// Package appauthz answers a company's own apps when they ask Grantra "may
// this person do this?". Each application has Cedar policies, written in the
// console; the person in the question is filled in from what Grantra already
// knows about them: their department and title, their manager, their
// business roles and their entitlements in that application.
//
// Apps ask with an API key of the application (see Keys), so each app is
// only ever judged by its own policies.
package appauthz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cedar-policy/cedar-go"
	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// PersonType is the Cedar entity type of the person asking.
const PersonType = "Person"

// MaxPolicyBytes bounds an application's policies.
const MaxPolicyBytes = 128 << 10

// Policies are an application's Cedar policies.
type Policies struct {
	Source    string     `json:"policies"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Parse reads policies. Each policy is known by its @id annotation, or by
// its position ("policy0", "policy1"...) when it has none.
func Parse(src string) (*cedar.PolicySet, error) {
	if len(src) > MaxPolicyBytes {
		return nil, apperr.Invalidf("policies are larger than %d KiB", MaxPolicyBytes>>10)
	}
	list, err := cedar.NewPolicyListFromBytes("policies.cedar", []byte(src))
	if err != nil {
		return nil, apperr.Invalidf("%v", err)
	}
	set := cedar.NewPolicySet()
	for i, p := range list {
		id := p.Annotations()["id"]
		if id == "" {
			id = cedar.String(fmt.Sprintf("policy%d", i))
		}
		if !set.Add(cedar.PolicyID(id), p) {
			return nil, apperr.Invalidf("line %d: two policies have the id %q", p.Position().Line, id)
		}
	}
	return set, nil
}

// GetPolicies returns an application's policies; none yet is empty.
func GetPolicies(ctx context.Context, tx pgx.Tx, appID string) (Policies, error) {
	if err := appExists(ctx, tx, appID); err != nil {
		return Policies{}, err
	}
	var p Policies
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT policies, updated_by, updated_at FROM app_authz_policies WHERE application_id = $1`, appID).
		Scan(&p.Source, &p.UpdatedBy, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policies{}, nil
	}
	p.UpdatedAt = &at
	return p, err
}

// SetPolicies replaces an application's policies, once they parse.
func SetPolicies(ctx context.Context, tx pgx.Tx, tenantID, appID, src, actor string) error {
	if err := appExists(ctx, tx, appID); err != nil {
		return err
	}
	if _, err := Parse(src); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO app_authz_policies (application_id, tenant_id, policies, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (application_id) DO UPDATE SET policies = $3, updated_by = $4, updated_at = now()`,
		appID, tenantID, src, actor)
	if err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "app_authz.policies_set", "application", appID,
		map[string]any{"bytes": len(src)})
}

func appExists(ctx context.Context, tx pgx.Tx, appID string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE id::text = $1)`, appID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return apperr.ErrNotFound
	}
	return nil
}

// Principal says who the question is about. Apps name people the way they
// know them: Grantra's id, their email, their employee id, or their username
// in this application.
type Principal struct {
	ID         string `json:"id,omitempty"`
	Email      string `json:"email,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Username   string `json:"username,omitempty"`
}

// Resource is what the person wants to act on, as the app models it: a type
// and id, such as Invoice "42", and the attributes the policies look at.
type Resource struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

// Question is one authorization question.
type Question struct {
	Principal Principal       `json:"principal"`
	Action    string          `json:"action"`
	Resource  Resource        `json:"resource"`
	Context   json.RawMessage `json:"context,omitempty"`
}

// Answer is Grantra's answer.
type Answer struct {
	Allowed bool `json:"allowed"`
	// Policies are the ids of the policies that decided: the permits that
	// allowed, or the forbids that denied.
	Policies []string `json:"policies"`
	// Errors are policies that failed to evaluate, which Cedar skips.
	Errors []string `json:"errors,omitempty"`
	// Reason says why Grantra denied without asking the policies.
	Reason string `json:"reason,omitempty"`
}

// Person is what the policies see of the person, as principal.
type Person struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Kind        string `json:"kind"`
	ManagerID   string `json:"manager_id,omitempty"`
	// ManagerEmail is the manager's, which policies read as
	// principal.manager.email.
	ManagerEmail string            `json:"manager_email,omitempty"`
	Attributes   map[string]string `json:"attributes"`
	// Roles are the business roles assigned to the person.
	Roles []string `json:"roles"`
	// Entitlements are the native ids of the application's entitlements the
	// person holds there or is assigned in Grantra.
	Entitlements []string `json:"entitlements"`
}

// Denials Grantra makes before the policies.
const (
	ReasonUnknown  = "unknown_person"
	ReasonInactive = "person_not_active"
)

// FindPerson resolves a principal for an application. A person who is not
// found is nil, with no error.
func FindPerson(ctx context.Context, tx pgx.Tx, appID string, p Principal) (*Person, error) {
	var where, arg string
	switch {
	case p.ID != "":
		where, arg = `i.id::text = $2`, p.ID
	case p.Email != "":
		where, arg = `lower(i.email) = lower($2)`, p.Email
	case p.ExternalID != "":
		where, arg = `i.external_id = $2`, p.ExternalID
	case p.Username != "":
		where, arg = `i.id = (SELECT identity_id FROM accounts WHERE application_id::text = $1 AND lower(username) = lower($2) AND status <> 'deleted' LIMIT 1)`, p.Username
	default:
		return nil, apperr.Invalidf("principal needs one of id, email, external_id or username")
	}
	var (
		person Person
		attrs  map[string]any
	)
	err := tx.QueryRow(ctx, `
		SELECT i.id, coalesce(i.email, ''), i.display_name, i.status, i.kind, coalesce(i.manager_id::text, ''), coalesce(m.email, ''), i.attributes
		FROM identities i LEFT JOIN identities m ON m.id = i.manager_id
		WHERE `+where+` AND $1::text IS NOT NULL ORDER BY i.created_at LIMIT 1`, appID, arg).
		Scan(&person.ID, &person.Email, &person.DisplayName, &person.Status, &person.Kind, &person.ManagerID, &person.ManagerEmail, &attrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	person.Attributes = map[string]string{}
	for k, v := range attrs {
		if s, ok := v.(string); ok {
			person.Attributes[k] = s
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT r.name FROM access_assignments x JOIN roles r ON r.id = x.role_id
		WHERE x.identity_id = $1 AND (x.expires_at IS NULL OR x.expires_at > now()) ORDER BY 1`, person.ID)
	if err != nil {
		return nil, err
	}
	if person.Roles, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `
		SELECT e.native_id FROM account_entitlements ae
		JOIN accounts a ON a.id = ae.account_id
		JOIN entitlements e ON e.id = ae.entitlement_id
		WHERE a.identity_id = $1 AND a.application_id::text = $2 AND a.status = 'active'
		UNION
		SELECT e.native_id FROM access_assignments x
		LEFT JOIN role_entitlements re ON re.role_id = x.role_id
		JOIN entitlements e ON e.id = coalesce(x.entitlement_id, re.entitlement_id)
		WHERE x.identity_id = $1 AND e.application_id::text = $2 AND (x.expires_at IS NULL OR x.expires_at > now())
		ORDER BY 1`, person.ID, appID)
	if err != nil {
		return nil, err
	}
	if person.Entitlements, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	return &person, nil
}

// entity is the person as a Cedar entity. Attributes the HR source sends,
// such as department, title and location, are top-level attributes too
// unless they clash with Grantra's own.
func (p *Person) entity() cedar.Entity {
	set := func(ss []string) cedar.Set {
		vs := make([]cedar.Value, 0, len(ss))
		for _, s := range ss {
			vs = append(vs, cedar.String(s))
		}
		return cedar.NewSet(vs...)
	}
	attrs := cedar.RecordMap{}
	extra := cedar.RecordMap{}
	for k, v := range p.Attributes {
		extra[cedar.String(k)] = cedar.String(v)
		attrs[cedar.String(k)] = cedar.String(v)
	}
	attrs["email"] = cedar.String(p.Email)
	attrs["display_name"] = cedar.String(p.DisplayName)
	attrs["status"] = cedar.String(p.Status)
	attrs["kind"] = cedar.String(p.Kind)
	attrs["attributes"] = cedar.NewRecord(extra)
	attrs["roles"] = set(p.Roles)
	attrs["entitlements"] = set(p.Entitlements)
	if p.ManagerID != "" {
		attrs["manager"] = cedar.NewEntityUID(PersonType, cedar.String(p.ManagerID))
	}
	return cedar.Entity{UID: cedar.NewEntityUID(PersonType, cedar.String(p.ID)), Attributes: cedar.NewRecord(attrs)}
}

// Evaluate answers q about person with set. A person who is not active is
// denied whatever the policies say, so leavers lose access at once.
func Evaluate(set *cedar.PolicySet, person *Person, q Question) (Answer, error) {
	q.Action = strings.TrimSpace(q.Action)
	if q.Action == "" || q.Resource.Type == "" || q.Resource.ID == "" {
		return Answer{}, apperr.Invalidf("action, resource.type and resource.id are required")
	}
	if !validType(q.Resource.Type) {
		return Answer{}, apperr.Invalidf("resource.type %q must be a Cedar type name, such as Invoice or Billing::Invoice", q.Resource.Type)
	}
	if q.Resource.Type == PersonType || q.Resource.Type == "Action" {
		return Answer{}, apperr.Invalidf("resource.type %q is reserved", q.Resource.Type)
	}
	resAttrs, err := record("resource.attributes", q.Resource.Attributes)
	if err != nil {
		return Answer{}, err
	}
	ctxRec, err := record("context", q.Context)
	if err != nil {
		return Answer{}, err
	}
	if person == nil {
		return Answer{Policies: []string{}, Reason: ReasonUnknown}, nil
	}
	if person.Status != "active" {
		return Answer{Policies: []string{}, Reason: ReasonInactive}, nil
	}
	resource := cedar.NewEntityUID(cedar.EntityType(q.Resource.Type), cedar.String(q.Resource.ID))
	action := cedar.NewEntityUID("Action", cedar.String(q.Action))
	pe := person.entity()
	entities := cedar.EntityMap{
		action:   cedar.Entity{UID: action},
		resource: {UID: resource, Attributes: resAttrs},
	}
	if person.ManagerID != "" {
		m := cedar.NewEntityUID(PersonType, cedar.String(person.ManagerID))
		entities[m] = cedar.Entity{UID: m, Attributes: cedar.NewRecord(cedar.RecordMap{"email": cedar.String(person.ManagerEmail)})}
	}
	// The person goes last, in case the resource is the person too.
	entities[pe.UID] = pe
	decision, diag := set.IsAuthorized(entities, cedar.Request{
		Principal: pe.UID, Action: action, Resource: resource, Context: ctxRec,
	})
	a := Answer{Allowed: decision == cedar.Allow, Policies: []string{}}
	for _, r := range diag.Reasons {
		a.Policies = append(a.Policies, string(r.PolicyID))
	}
	for _, e := range diag.Errors {
		a.Errors = append(a.Errors, fmt.Sprintf("%s: %s", e.PolicyID, e.Message))
	}
	slices.Sort(a.Policies)
	return a, nil
}

// validType accepts Cedar entity type names: identifiers joined by "::".
func validType(t string) bool {
	for _, part := range strings.Split(t, "::") {
		if part == "" {
			return false
		}
		for i, r := range part {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

// record reads a JSON object in Cedar's JSON value format: plain JSON, with
// {"__entity": {"type": "T", "id": "x"}} for entities.
func record(field string, raw json.RawMessage) (cedar.Record, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return cedar.Record{}, nil
	}
	var r cedar.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return cedar.Record{}, apperr.Invalidf("%s must be a JSON object of Cedar values: %v", field, err)
	}
	return r, nil
}

// Check answers an app's question with its stored policies. No policies
// denies everything.
func Check(ctx context.Context, tx pgx.Tx, appID string, q Question) (Answer, error) {
	p, err := GetPolicies(ctx, tx, appID)
	if err != nil {
		return Answer{}, err
	}
	set, err := Parse(p.Source)
	if err != nil {
		return Answer{}, err
	}
	person, err := FindPerson(ctx, tx, appID, q.Principal)
	if err != nil {
		return Answer{}, err
	}
	return Evaluate(set, person, q)
}
