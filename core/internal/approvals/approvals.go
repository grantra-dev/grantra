// Package approvals decides who approves an access request. Admins set up
// approval policies in the console (Governance → Approval flows): each has
// conditions (the kind of request, the applications or roles it is for, how
// risky the access is, who it is for and for how long) and ordered steps
// (who approves, how many of them must, how long they have and what happens
// when they do not answer). Policies are tried in order and the first
// enabled one whose conditions match decides; with none, the built-in flow
// applies: the person's manager, then the owner of the role or application.
//
// Some rules no policy changes. Nobody approves their own access: they drop
// out of the step, which goes to the admins when nobody is left. Asking
// counts as approving when the requester is one of the approvers. The same
// approvers are not asked twice in a row. Only low risk access can be
// approved with no step at all. And separation of duties risks are always
// decided by an admin (package requests adds that step).
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/nhi"
)

// Who approves a step. They are also the kinds of request steps.
const (
	// Manager is the manager of the person the access is for, or a manager
	// further up (Level).
	Manager = "manager"
	// Owner is the owner of the role, or of the entitlement's application.
	Owner = "owner"
	// People are people named in the policy.
	People = "people"
	// Holders are the people who hold a business role, such as "IT security".
	Holders = "holders"
	// Admin is any Grantra admin.
	Admin = "admin"
)

// What happens to a step nobody decided in time.
const (
	// Expire closes the request as expired (the default).
	Expire = "expire"
	// Escalate asks the approvers' managers once, with the same time again;
	// with none, the admins.
	Escalate = "escalate"
	// Reject rejects the request.
	Reject = "reject"
)

// Request kinds a policy can match, as package requests names them.
var requestKinds = []string{"access", "eligibility", "activation"}

// identityKinds are the kinds of identity a policy can match.
var identityKinds = append([]string{"human"}, nhi.Kinds...)

// Limits on a policy.
const (
	maxSteps     = 10
	maxPeople    = 20
	maxLevel     = 5
	maxTimeout   = 720
	maxListItems = 50
)

// Person is someone asked to approve.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Step is one approval a policy asks for.
type Step struct {
	Approver string `json:"approver"`
	// Level, for Manager, is how far up: 1 is the manager (the default), 2
	// the manager's manager, up to 5.
	Level int `json:"level,omitempty"`
	// PeopleIDs, for People, are who is asked.
	PeopleIDs []string `json:"people_ids,omitempty"`
	// RoleID, for Holders, is the business role whose holders are asked.
	RoleID string `json:"role_id,omitempty"`
	// Required is how many of the approvers must approve (1 when not set);
	// All asks every one of them.
	Required int  `json:"required,omitempty"`
	All      bool `json:"all,omitempty"`
	// TimeoutHours is how long the step waits, 1 to 720; when not set, the
	// tenant's settings.RequestStepTimeoutHours.
	TimeoutHours int    `json:"timeout_hours,omitempty"`
	OnTimeout    string `json:"on_timeout,omitempty"`
	// Label names the step for the people who see the flow, such as
	// "Payroll team".
	Label string `json:"label,omitempty"`
	// People names PeopleIDs when reading.
	People []Person `json:"people,omitempty"`
}

// Conditions say which requests a policy is for. Each one that is set must
// match; a policy with none matches every request.
type Conditions struct {
	// Kinds of request: access, eligibility or activation.
	Kinds []string `json:"kinds,omitempty"`
	// ApplicationIDs: an entitlement of one of these applications, or a
	// role that bundles one.
	ApplicationIDs []string `json:"application_ids,omitempty"`
	// RoleIDs: one of these business roles.
	RoleIDs []string `json:"role_ids,omitempty"`
	// Risks of the access (a role's is its riskiest entitlement's).
	Risks []string `json:"risks,omitempty"`
	// Departments of the person the access is for, ignoring case.
	Departments []string `json:"departments,omitempty"`
	// IdentityKinds of the person the access is for: human, service, agent
	// or workload.
	IdentityKinds []string `json:"identity_kinds,omitempty"`
	// LongerThanDays: access asked for longer than this many days, or with
	// no end.
	LongerThanDays int `json:"longer_than_days,omitempty"`
}

// Policy is an approval policy.
type Policy struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Position    int        `json:"position"`
	Enabled     bool       `json:"enabled"`
	Conditions  Conditions `json:"conditions"`
	Steps       []Step     `json:"steps"`
	Version     int        `json:"version"`
	UpdatedBy   string     `json:"updated_by"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Builtin is the flow of requests no policy matches.
var Builtin = Policy{Name: "Built-in flow", Enabled: true, Steps: []Step{{Approver: Manager}, {Approver: Owner}}}

// Input is a policy to save.
type Input struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     *bool      `json:"enabled"`
	Conditions  Conditions `json:"conditions"`
	Steps       []Step     `json:"steps"`
}

// clean trims a list of words and drops the empty ones and repeats.
func clean(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// oneOf checks that every value of list is in allowed.
func oneOf(field string, list, allowed []string) error {
	for _, v := range list {
		if !slices.Contains(allowed, v) {
			return apperr.Invalidf("%s: %q is not one of %s", field, v, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// exist checks that every id of list is a row of table.
func exist(ctx context.Context, tx pgx.Tx, table, what string, ids []string) error {
	for _, id := range ids {
		if !db.ValidUUID(id) {
			return apperr.Invalidf("%s %s does not exist", what, id)
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = ANY($1::uuid[])`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return apperr.Invalidf("a %s of the policy does not exist", what)
	}
	return nil
}

func (in *Input) check(ctx context.Context, tx pgx.Tx) error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || len(in.Name) > 200 {
		return apperr.Invalidf("name is required, at most 200 characters")
	}
	if len(in.Description) > 2000 {
		return apperr.Invalidf("description is longer than 2000 characters")
	}
	c := &in.Conditions
	c.Kinds, c.Risks, c.IdentityKinds = clean(c.Kinds), clean(c.Risks), clean(c.IdentityKinds)
	c.ApplicationIDs, c.RoleIDs, c.Departments = clean(c.ApplicationIDs), clean(c.RoleIDs), clean(c.Departments)
	if err := oneOf("kinds", c.Kinds, requestKinds); err != nil {
		return err
	}
	if err := oneOf("risks", c.Risks, catalog.Risks); err != nil {
		return err
	}
	if err := oneOf("identity_kinds", c.IdentityKinds, identityKinds); err != nil {
		return err
	}
	for _, l := range [][]string{c.ApplicationIDs, c.RoleIDs, c.Departments} {
		if len(l) > maxListItems {
			return apperr.Invalidf("a condition lists at most %d values", maxListItems)
		}
	}
	if err := exist(ctx, tx, "applications", "application", c.ApplicationIDs); err != nil {
		return err
	}
	if err := exist(ctx, tx, "roles", "role", c.RoleIDs); err != nil {
		return err
	}
	if c.LongerThanDays < 0 || c.LongerThanDays > 365 {
		return apperr.Invalidf("longer_than_days must be between 0 and 365")
	}
	if len(in.Steps) > maxSteps {
		return apperr.Invalidf("a policy has at most %d steps", maxSteps)
	}
	if len(in.Steps) == 0 && (len(c.Risks) == 0 || slices.ContainsFunc(c.Risks, func(r string) bool { return r != "low" })) {
		return apperr.Invalidf("only low risk access may be approved with no step: add a step, or limit the policy to low risk access")
	}
	for i := range in.Steps {
		if err := in.Steps[i].check(ctx, tx); err != nil {
			var bad *apperr.Invalid
			if errors.As(err, &bad) {
				return apperr.Invalidf("step %d: %s", i+1, bad.Msg)
			}
			return err
		}
	}
	return nil
}

func (s *Step) check(ctx context.Context, tx pgx.Tx) error {
	s.Label, s.People = strings.TrimSpace(s.Label), nil
	if len(s.Label) > 100 {
		return apperr.Invalidf("label is longer than 100 characters")
	}
	// Only what the kind of approver uses is kept.
	level, people, role := s.Level, clean(s.PeopleIDs), s.RoleID
	s.Level, s.PeopleIDs, s.RoleID = 0, nil, ""
	switch s.Approver {
	case Manager:
		if level < 0 || level > maxLevel {
			return apperr.Invalidf("level must be between 1 and %d", maxLevel)
		}
		s.Level = max(level, 1)
	case People:
		if len(people) == 0 || len(people) > maxPeople {
			return apperr.Invalidf("name 1 to %d people", maxPeople)
		}
		if err := exist(ctx, tx, "identities", "person", people); err != nil {
			return err
		}
		s.PeopleIDs = people
	case Holders:
		if err := exist(ctx, tx, "roles", "role", []string{role}); err != nil {
			return err
		}
		s.RoleID = role
	case Owner, Admin:
	default:
		return apperr.Invalidf("approver must be manager, owner, people, holders or admin")
	}
	if s.Required < 0 || s.Required > maxPeople {
		return apperr.Invalidf("required must be between 1 and %d", maxPeople)
	}
	if s.All && s.Required > 1 {
		return apperr.Invalidf("ask for all of the approvers or for a number of them, not both")
	}
	if s.Approver == People && s.Required > len(s.PeopleIDs) {
		return apperr.Invalidf("required is more than the people named")
	}
	if s.Approver == Manager || s.Approver == Owner || s.Approver == Admin {
		// There is one manager and one owner, and any one admin decides.
		s.Required, s.All = 0, false
	}
	if s.Required == 1 {
		s.Required = 0
	}
	if s.TimeoutHours < 0 || s.TimeoutHours > maxTimeout {
		return apperr.Invalidf("timeout_hours must be between 1 and %d", maxTimeout)
	}
	switch s.OnTimeout {
	case "":
		s.OnTimeout = Expire
	case Expire, Reject:
	case Escalate:
		if s.Approver == Admin {
			return apperr.Invalidf("a step the admins decide cannot be escalated")
		}
	default:
		return apperr.Invalidf("on_timeout must be expire, escalate or reject")
	}
	return nil
}

// Save creates (id empty) or replaces a policy. A new policy goes last.
func Save(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in Input) (Policy, error) {
	if err := in.check(ctx, tx); err != nil {
		return Policy{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	conditions, err := json.Marshal(in.Conditions)
	if err != nil {
		return Policy{}, err
	}
	steps, err := json.Marshal(in.Steps)
	if err != nil {
		return Policy{}, err
	}
	if id == "" {
		err = tx.QueryRow(ctx,
			`INSERT INTO approval_policies (tenant_id, name, description, position, enabled, conditions, steps, updated_by)
			 VALUES ($1, $2, nullif($3, ''), (SELECT coalesce(max(position), 0) + 1 FROM approval_policies), $4, $5, $6, $7)
			 RETURNING id`, tenantID, in.Name, in.Description, enabled, conditions, steps, actor).Scan(&id)
	} else {
		if !db.ValidUUID(id) {
			return Policy{}, apperr.ErrNotFound
		}
		err = tx.QueryRow(ctx,
			`UPDATE approval_policies SET name = $2, description = nullif($3, ''), enabled = $4, conditions = $5, steps = $6,
			        updated_by = $7, updated_at = now(), version = version + 1
			 WHERE id = $1 RETURNING id`, id, in.Name, in.Description, enabled, conditions, steps, actor).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return Policy{}, apperr.ErrNotFound
		}
	}
	if db.IsUniqueViolation(err) {
		return Policy{}, apperr.Conflictf("there is already an approval policy named %q", in.Name)
	}
	if err != nil {
		return Policy{}, err
	}
	p, err := Get(ctx, tx, id)
	if err != nil {
		return Policy{}, err
	}
	return p, db.Audit(ctx, tx, tenantID, actor, "approval_policy.saved", "approval_policy", id, map[string]any{"policy": p})
}

// Delete removes a policy. Requests that followed it keep its name and
// version.
func Delete(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	p, err := Get(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM approval_policies WHERE id = $1`, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "approval_policy.deleted", "approval_policy", id, map[string]any{"policy": p})
}

// Reorder sets the order policies are tried in: ids names every policy,
// first to last.
func Reorder(ctx context.Context, tx pgx.Tx, tenantID, actor string, ids []string) error {
	var all []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM approval_policies ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	if all, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return err
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	if !slices.Equal(sorted, all) {
		return apperr.Invalidf("ids must name every approval policy once")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval_policies p SET position = o.n FROM unnest($1::uuid[]) WITH ORDINALITY o(id, n) WHERE p.id = o.id`, ids); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "approval_policy.reordered", "approval_policies", "tenant", map[string]any{"order": ids})
}

// Get returns one policy.
func Get(ctx context.Context, tx pgx.Tx, id string) (Policy, error) {
	if !db.ValidUUID(id) {
		return Policy{}, apperr.ErrNotFound
	}
	list, err := list(ctx, tx, id, false)
	if err != nil {
		return Policy{}, err
	}
	if len(list) == 0 {
		return Policy{}, apperr.ErrNotFound
	}
	return list[0], nil
}

// List returns every policy, in the order they are tried.
func List(ctx context.Context, tx pgx.Tx) ([]Policy, error) { return list(ctx, tx, "", false) }

func list(ctx context.Context, tx pgx.Tx, id string, enabledOnly bool) ([]Policy, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, coalesce(description, ''), position, enabled, conditions, steps, version, updated_by, updated_at
		 FROM approval_policies
		 WHERE ($1 = '' OR id = nullif($1, '')::uuid) AND (enabled OR NOT $2)
		 ORDER BY position, name`, id, enabledOnly)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Policy, error) {
		var p Policy
		err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Position, &p.Enabled, &p.Conditions, &p.Steps, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	return out, namePeople(ctx, tx, out)
}

// namePeople fills in the names of the people the steps of list name.
func namePeople(ctx context.Context, tx pgx.Tx, list []Policy) error {
	var ids []string
	for _, p := range list {
		for _, s := range p.Steps {
			ids = append(ids, s.PeopleIDs...)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT id::text, display_name FROM identities WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range list {
		for j := range list[i].Steps {
			s := &list[i].Steps[j]
			for _, id := range s.PeopleIDs {
				s.People = append(s.People, Person{ID: id, Name: names[id]})
			}
		}
	}
	return nil
}

// Draft checks a policy that is not saved, to try with ResolveWith.
func Draft(ctx context.Context, tx pgx.Tx, in Input) (Policy, error) {
	if err := in.check(ctx, tx); err != nil {
		return Policy{}, err
	}
	p := Policy{Name: in.Name, Description: in.Description, Enabled: true, Conditions: in.Conditions, Steps: in.Steps}
	list := []Policy{p}
	err := namePeople(ctx, tx, list)
	return list[0], err
}
