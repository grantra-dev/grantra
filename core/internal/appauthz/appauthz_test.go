package appauthz

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/apperr"
)

func TestParse(t *testing.T) {
	set, err := Parse(`@id("a") permit (principal, action, resource); permit (principal, action, resource);`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for id := range set.All() {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"a", "policy1"}) {
		t.Errorf("ids = %v", ids)
	}
	for _, bad := range []string{
		`permit (principal`,
		`@id("a") permit (principal, action, resource); @id("a") forbid (principal, action, resource);`,
		string(make([]byte, MaxPolicyBytes+1)),
	} {
		var inv *apperr.Invalid
		if _, err := Parse(bad); !errors.As(err, &inv) {
			t.Errorf("parse %.40q = %v", bad, err)
		}
	}
}

func TestEvaluate(t *testing.T) {
	set, err := Parse(`permit (principal, action == Action::"read", resource is Docs::Page)
when { principal.roles.contains("Writers") && context.ip == "10.0.0.1" && resource.space == principal.attributes.team };`)
	if err != nil {
		t.Fatal(err)
	}
	ana := &Person{ID: "p1", Status: "active", Roles: []string{"Writers"}, Attributes: map[string]string{"team": "core"}}
	q := Question{Action: "read", Resource: Resource{Type: "Docs::Page", ID: "1", Attributes: json.RawMessage(`{"space": "core"}`)},
		Context: json.RawMessage(`{"ip": "10.0.0.1"}`)}
	if a, err := Evaluate(set, ana, q); err != nil || !a.Allowed || !slices.Equal(a.Policies, []string{"policy0"}) {
		t.Errorf("allowed = %+v, %v", a, err)
	}
	q.Context = json.RawMessage(`{"ip": "10.0.0.2"}`)
	if a, _ := Evaluate(set, ana, q); a.Allowed {
		t.Error("allowed from another ip")
	}
	if a, _ := Evaluate(set, nil, q); a.Allowed || a.Reason != ReasonUnknown {
		t.Errorf("unknown = %+v", a)
	}
	gone := *ana
	gone.Status = "suspended"
	q.Context = nil
	if a, _ := Evaluate(set, &gone, q); a.Allowed || a.Reason != ReasonInactive {
		t.Errorf("suspended = %+v", a)
	}
	owner, _ := Parse(`permit (principal, action, resource) when { resource.owner == principal && principal.manager.email == "boss@example.com" };`)
	boss := *ana
	boss.ManagerID, boss.ManagerEmail = "p0", "boss@example.com"
	q.Resource.Attributes = json.RawMessage(`{"owner": {"__entity": {"type": "Person", "id": "p1"}}}`)
	if a, err := Evaluate(owner, &boss, q); err != nil || !a.Allowed {
		t.Errorf("entity attribute = %+v, %v", a, err)
	}
	for _, typ := range []string{"", "1Page", "Docs::", "a-b", "Person", "Action"} {
		q.Resource.Type = typ
		var inv *apperr.Invalid
		if _, err := Evaluate(set, ana, q); !errors.As(err, &inv) {
			t.Errorf("type %q = %v", typ, err)
		}
	}
}

// Check parses each version of an app's policies once.
func TestParseCached(t *testing.T) {
	src := `@id("readers") permit (principal, action == Action::"read", resource);`
	a, err := parseCached(src)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := parseCached(src); b != a {
		t.Error("the same policies were parsed again")
	}
	if c, _ := parseCached(src + "\n"); c == a {
		t.Error("changed policies came from the cache")
	}
	if _, err := parseCached("permit ("); err == nil {
		t.Error("broken policies parsed")
	}
	if _, err := parseCached("permit ("); err == nil {
		t.Error("broken policies were cached")
	}
}
