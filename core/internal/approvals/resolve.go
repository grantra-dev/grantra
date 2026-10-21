package approvals

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
)

// Request is what a plan is made for: what is asked, by whom and for whom.
type Request struct {
	// Kind is access, eligibility or activation.
	Kind          string
	RoleID        string
	EntitlementID string
	RequesterID   string
	BeneficiaryID string
	DurationDays  *int
	DurationHours *int
}

// Planned is a step of a plan, with the people it asks.
type Planned struct {
	// Kind is who approves (Manager, Owner, People, Holders or Admin).
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	// Approvers are who is asked; none means any admin decides.
	Approvers []Person `json:"approvers"`
	// Required is how many approvers must approve.
	Required int `json:"required"`
	// TimeoutHours is how long the step waits; 0 means the tenant's
	// settings.RequestStepTimeoutHours.
	TimeoutHours int    `json:"timeout_hours,omitempty"`
	OnTimeout    string `json:"on_timeout"`
	// RequesterApproves says the requester is one of the approvers: asking
	// counts as their approval.
	RequesterApproves bool `json:"requester_approves,omitempty"`
	// Note says why the step is not what the policy says, such as nobody
	// being left to ask.
	Note string `json:"note,omitempty"`
}

// IDs are the ids of the step's approvers.
func (p Planned) IDs() []string {
	ids := make([]string, len(p.Approvers))
	for i, a := range p.Approvers {
		ids[i] = a.ID
	}
	return ids
}

// Plan is the approval flow of a request: the policy it follows and the
// steps it takes.
type Plan struct {
	// PolicyID and PolicyName are empty for the built-in flow;
	// PolicyID is empty for a draft too.
	PolicyID      string    `json:"policy_id,omitempty"`
	PolicyName    string    `json:"policy_name,omitempty"`
	PolicyVersion int       `json:"policy_version,omitempty"`
	Risk          string    `json:"risk"`
	Steps         []Planned `json:"steps"`
}

// facts are what the conditions and steps of a policy look at.
type facts struct {
	identityKind, department, manager string
	owner, risk                       string
	applications                      []string
}

func load(ctx context.Context, tx pgx.Tx, r Request) (facts, error) {
	var f facts
	err := tx.QueryRow(ctx,
		`SELECT kind, coalesce(trim(attributes ->> 'department'), ''), coalesce(manager_id::text, '') FROM identities WHERE id = $1`,
		r.BeneficiaryID).Scan(&f.identityKind, &f.department, &f.manager)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, apperr.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	// A role is as risky as its riskiest entitlement, and touches every
	// application it bundles entitlements of.
	if r.RoleID != "" {
		err = tx.QueryRow(ctx,
			`SELECT coalesce(r.owner_id::text, ''),
			        coalesce((SELECT e.risk FROM role_entitlements re JOIN entitlements e ON e.id = re.entitlement_id
			                  WHERE re.role_id = r.id ORDER BY array_position($2::text[], e.risk) DESC LIMIT 1), 'low'),
			        array(SELECT DISTINCT e.application_id::text FROM role_entitlements re JOIN entitlements e ON e.id = re.entitlement_id
			              WHERE re.role_id = r.id)
			 FROM roles r WHERE r.id = $1`, r.RoleID, catalog.Risks).Scan(&f.owner, &f.risk, &f.applications)
	} else {
		var app string
		err = tx.QueryRow(ctx,
			`SELECT coalesce(a.owner_id::text, ''), e.risk, a.id::text FROM entitlements e JOIN applications a ON a.id = e.application_id
			 WHERE e.id = $1`, r.EntitlementID).Scan(&f.owner, &f.risk, &app)
		f.applications = []string{app}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return f, apperr.ErrNotFound
	}
	return f, err
}

// matches says whether the conditions c hold for r.
func (c Conditions) matches(r Request, f facts) bool {
	anyOf := func(list []string, v ...string) bool {
		return len(list) == 0 || slices.ContainsFunc(v, func(x string) bool { return slices.Contains(list, x) })
	}
	if !anyOf(c.Kinds, r.Kind) || !anyOf(c.Risks, f.risk) || !anyOf(c.IdentityKinds, f.identityKind) ||
		!anyOf(c.ApplicationIDs, f.applications...) {
		return false
	}
	if len(c.RoleIDs) > 0 && (r.RoleID == "" || !slices.Contains(c.RoleIDs, r.RoleID)) {
		return false
	}
	if len(c.Departments) > 0 && !slices.ContainsFunc(c.Departments, func(d string) bool { return strings.EqualFold(d, f.department) }) {
		return false
	}
	if c.LongerThanDays > 0 {
		switch {
		case r.DurationDays != nil && *r.DurationDays <= c.LongerThanDays:
			return false
		case r.DurationHours != nil && *r.DurationHours <= c.LongerThanDays*24:
			return false
		}
	}
	return true
}

// Resolve works out the plan of r: the first enabled policy that matches
// it, or the built-in flow, with the people each step asks.
func Resolve(ctx context.Context, tx pgx.Tx, r Request) (Plan, error) {
	return ResolveWith(ctx, tx, r, nil)
}

// ResolveWith is Resolve with draft, when given, tried before every
// policy: what a policy being edited would do.
func ResolveWith(ctx context.Context, tx pgx.Tx, r Request, draft *Policy) (Plan, error) {
	f, err := load(ctx, tx, r)
	if err != nil {
		return Plan{}, err
	}
	policies, err := list(ctx, tx, "", true)
	if err != nil {
		return Plan{}, err
	}
	if draft != nil {
		policies = append([]Policy{*draft}, policies...)
	}
	use, builtin := Builtin, true
	for _, p := range policies {
		if p.Conditions.matches(r, f) {
			use, builtin = p, false
			break
		}
	}
	plan := Plan{Risk: f.risk, Steps: []Planned{}}
	if !builtin {
		plan.PolicyID, plan.PolicyName, plan.PolicyVersion = use.ID, use.Name, use.Version
	}
	for _, s := range use.Steps {
		p, err := planStep(ctx, tx, r, f, s)
		if err != nil {
			return Plan{}, err
		}
		// The same approvers are asked once.
		if n := len(plan.Steps); n > 0 && slices.Equal(plan.Steps[n-1].IDs(), p.IDs()) && p.Required <= plan.Steps[n-1].Required {
			continue
		}
		plan.Steps = append(plan.Steps, p)
	}
	if len(plan.Steps) == 0 && f.risk != "low" {
		plan.Steps = append(plan.Steps, Planned{Kind: Admin, Approvers: []Person{}, Required: 1, OnTimeout: Expire,
			Note: f.risk + " risk access needs someone to approve it"})
	}
	return plan, nil
}

// planStep finds who s asks about r.
func planStep(ctx context.Context, tx pgx.Tx, r Request, f facts, s Step) (Planned, error) {
	p := Planned{Kind: s.Approver, Label: s.Label, TimeoutHours: s.TimeoutHours, OnTimeout: s.OnTimeout, Approvers: []Person{}}
	if p.OnTimeout == "" {
		p.OnTimeout = Expire
	}
	var (
		ids []string
		err error
	)
	switch s.Approver {
	case Manager:
		ids, err = managerAt(ctx, tx, f.manager, max(s.Level, 1))
	case Owner:
		if f.owner != "" {
			ids = []string{f.owner}
		}
	case People:
		ids = s.PeopleIDs
	case Holders:
		ids, err = holders(ctx, tx, s.RoleID)
	}
	if err != nil {
		return p, err
	}
	people, err := active(ctx, tx, ids)
	if err != nil {
		return p, err
	}
	// Nobody approves their own access.
	if i := slices.IndexFunc(people, func(x Person) bool { return x.ID == r.BeneficiaryID }); i >= 0 {
		people = slices.Delete(people, i, i+1)
		p.Note = "the person the access is for does not approve it"
	}
	if s.Approver != Admin && len(people) == 0 {
		p.Kind, p.OnTimeout = Admin, Expire
		if p.Note == "" {
			p.Note = "nobody to ask, so an admin decides"
		} else {
			p.Note += ", so an admin decides"
		}
	}
	p.Approvers = people
	switch {
	case len(people) == 0:
		p.Required = 1
	case s.All:
		p.Required = len(people)
	default:
		p.Required = min(max(s.Required, 1), len(people))
		if s.Required > len(people) && p.Note == "" {
			p.Note = "fewer people to ask than the policy wants"
		}
	}
	p.RequesterApproves = r.RequesterID != r.BeneficiaryID && slices.ContainsFunc(people, func(x Person) bool { return x.ID == r.RequesterID })
	return p, nil
}

// managerAt is the manager level steps up from manager (level 1).
func managerAt(ctx context.Context, tx pgx.Tx, manager string, level int) ([]string, error) {
	for ; level > 1 && manager != ""; level-- {
		if err := tx.QueryRow(ctx, `SELECT coalesce(manager_id::text, '') FROM identities WHERE id = $1`, manager).Scan(&manager); err != nil {
			return nil, err
		}
	}
	if manager == "" {
		return nil, nil
	}
	return []string{manager}, nil
}

// maxHolders keeps a step that asks the holders of a role to a list anyone
// can be emailed about.
const maxHolders = 100

// holders are the people who hold role now.
func holders(ctx context.Context, tx pgx.Tx, role string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT identity_id::text FROM access_assignments
		 WHERE role_id = $1 AND (expires_at IS NULL OR expires_at > now()) LIMIT $2`, role, maxHolders)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// active names the people of ids who are active, by name.
func active(ctx context.Context, tx pgx.Tx, ids []string) ([]Person, error) {
	if len(ids) == 0 {
		return []Person{}, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id::text, display_name FROM identities WHERE id = ANY($1::uuid[]) AND status = 'active' AND kind = 'human'
		 ORDER BY display_name, id`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		err := row.Scan(&p.ID, &p.Name)
		return p, err
	})
}
