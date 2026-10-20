// Package attackpaths works out who can reach the riskiest access, and
// how: an assignment, a role, a birthright rule, an eligibility, an account
// that holds it in the application, or a shared account's credential that
// the vault or the gateway hands out. The last two are the paths people
// miss: whoever may check out the password of an account that holds Domain
// Admins reaches Domain Admins, whatever their own access says.
package attackpaths

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// maxDepth bounds how many credentials a path may go through: the
// password of an account that holds the gateway target whose account
// holds the target is two.
const maxDepth = 3

// MaxPaths is the most paths a target's detail lists.
const MaxPaths = 1000

// Step kinds.
const (
	KindPerson        = "person"
	KindRule          = "rule"
	KindRole          = "role"
	KindEntitlement   = "entitlement"
	KindAccount       = "account"
	KindVaultAccount  = "vault_account"
	KindGatewayTarget = "gateway_target"
)

// Edges: how a step leads to the next one.
const (
	EdgeAssigned  = "assigned"
	EdgeRequested = "requested"
	EdgeActivated = "activated"
	EdgeEligible  = "eligible"
	EdgeMatches   = "matches"
	EdgeGives     = "gives"
	EdgeIncludes  = "includes"
	EdgeOwns      = "owns"
	EdgeHolds     = "holds"
	EdgeLogsInAs  = "logs_in_as"
)

// Step is one node of a path.
type Step struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Detail is the application of an entitlement or an account.
	Detail string `json:"detail,omitempty"`
	// Edge is how the step before leads to this one; empty on the first.
	Edge string `json:"edge,omitempty"`
}

// Path is one way a person reaches a target, from the person to it.
type Path struct {
	IdentityID   string `json:"identity_id"`
	IdentityName string `json:"identity_name"`
	// Standing says nothing ends it: no end date, and nothing to turn on.
	Standing bool `json:"standing"`
	// Outside says part of it was not given by Grantra: an account that
	// holds the access in the application without an assignment for it.
	Outside bool `json:"outside"`
	// Credential says it goes through a shared account's credential.
	Credential bool `json:"credential"`
	// Until is when it ends, for a path that ends.
	Until *time.Time `json:"until,omitempty"`
	Steps []Step     `json:"steps"`
}

// Target is access worth an attacker's while: an entitlement of high or
// critical risk, and how many people reach it.
type Target struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Application string `json:"application"`
	Risk        string `json:"risk"`
	JITOnly     bool   `json:"jit_only"`
	// People is how many active people reach it; Standing, Credential and
	// Outside how many of them have a path of that kind.
	People     int `json:"people"`
	Standing   int `json:"standing"`
	Credential int `json:"credential"`
	Outside    int `json:"outside"`
	// Orphans is how many active accounts nobody active owns hold it.
	Orphans int `json:"orphans"`
}

// Orphan is an active account that holds a target and belongs to no
// active person, nor is a vault account Grantra hands out.
type Orphan struct {
	AccountID   string `json:"account_id"`
	Account     string `json:"account"`
	Application string `json:"application"`
	// Owner is who the account belonged to, when they left or are
	// suspended.
	Owner       string `json:"owner,omitempty"`
	OwnerStatus string `json:"owner_status,omitempty"`
}

// Detail is a target and every way to it.
type Detail struct {
	Target
	Paths []Path `json:"paths"`
	// Truncated says there were more than MaxPaths paths.
	Truncated bool `json:"truncated"`
	// OrphanAccounts are the accounts Orphans counts.
	OrphanAccounts []Orphan `json:"orphan_accounts"`
}

// Reach is a target one person reaches, and how.
type Reach struct {
	Target
	Paths []Path `json:"paths"`
}

type entitlement struct {
	id, name, app, risk string
	jitOnly, target     bool
}

// grant is access an active person was given to an entitlement: an
// assignment or an eligibility, directly or through a role.
type grant struct {
	identity, name   string
	roleID, roleName string
	ruleID, ruleName string
	edge             string
	until            *time.Time
}

type holding struct {
	accountID, account, app string
	builtin                 bool
	owner, ownerName        string
	ownerActive             bool
	ownerStatus             string
	until                   *time.Time
}

// control is a shared account's credential that holders of the
// entitlement ent get: a vault account, or a gateway target that logs in
// with one.
type control struct {
	ent            string
	kind, id, name string
}

// Graph is the access data paths are made of.
type Graph struct {
	ents     map[string]*entitlement
	grants   map[string][]grant   // by entitlement
	holdings map[string][]holding // by entitlement
	// controlled are the credentials that log in as an account, by account.
	controlled map[string][]control
	memo       map[string][]Path
}

// Load reads what paths are made of: the entitlements of high and critical
// risk and everything that leads to them.
func Load(ctx context.Context, tx pgx.Tx) (*Graph, error) {
	g := &Graph{ents: map[string]*entitlement{}, grants: map[string][]grant{}, holdings: map[string][]holding{},
		controlled: map[string][]control{}, memo: map[string][]Path{}}

	// The credentials, and the entitlements that give them.
	rows, err := tx.Query(ctx, `
		SELECT va.entitlement_id, $1, va.id, va.name, va.account_id FROM vault_accounts va WHERE va.account_id IS NOT NULL
		UNION ALL
		SELECT t.entitlement_id, $2, t.id, t.name, va.account_id
		FROM gateway_targets t JOIN vault_accounts va ON va.id = t.vault_account_id WHERE va.account_id IS NOT NULL`,
		KindVaultAccount, KindGatewayTarget)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var account string
		var c control
		if err := rows.Scan(&c.ent, &c.kind, &c.id, &c.name, &account); err != nil {
			rows.Close()
			return nil, err
		}
		g.controlled[account] = append(g.controlled[account], c)
		keys = append(keys, c.ent)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT e.id, e.name, app.name, e.risk, e.jit_only, `+catalog.RiskySQL("e.risk")+`
		FROM entitlements e JOIN applications app ON app.id = e.application_id
		WHERE `+catalog.RiskySQL("e.risk")+` OR e.id = ANY($1::uuid[])`, keys)
	if err != nil {
		return nil, err
	}
	keys = keys[:0]
	for rows.Next() {
		e := &entitlement{}
		if err := rows.Scan(&e.id, &e.name, &e.app, &e.risk, &e.jitOnly, &e.target); err != nil {
			rows.Close()
			return nil, err
		}
		g.ents[e.id] = e
		keys = append(keys, e.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Assignments and eligibilities of active people, directly or through
	// a role. given is inlined into each branch, so each reads only the
	// access to the targets, by index, instead of every assignment.
	rows, err = tx.Query(ctx, `
		WITH given AS NOT MATERIALIZED (
			SELECT x.identity_id, x.role_id, x.entitlement_id, x.origin, x.rule_id, x.expires_at
			FROM access_assignments x WHERE x.expires_at IS NULL OR x.expires_at > now()
			UNION ALL
			SELECT el.identity_id, el.role_id, el.entitlement_id, 'eligibility', NULL, el.expires_at
			FROM access_eligibilities el WHERE el.expires_at IS NULL OR el.expires_at > now()
		), reached AS (
			SELECT g.*, g.entitlement_id AS target FROM given g WHERE g.entitlement_id = ANY($1::uuid[])
			UNION ALL
			SELECT g.*, re.entitlement_id FROM given g JOIN role_entitlements re ON re.role_id = g.role_id
			WHERE re.entitlement_id = ANY($1::uuid[])
		)
		SELECT r.target, r.identity_id, i.display_name, coalesce(r.role_id::text, ''), coalesce(ro.name, ''),
		       r.origin, coalesce(r.rule_id::text, ''), coalesce(ru.name, ''), r.expires_at
		FROM reached r
		JOIN identities i ON i.id = r.identity_id
		LEFT JOIN roles ro ON ro.id = r.role_id
		LEFT JOIN access_rules ru ON ru.id = r.rule_id
		WHERE i.status = 'active'
		ORDER BY i.display_name, r.identity_id`, keys)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ent, origin string
		var gr grant
		if err := rows.Scan(&ent, &gr.identity, &gr.name, &gr.roleID, &gr.roleName, &origin, &gr.ruleID, &gr.ruleName, &gr.until); err != nil {
			rows.Close()
			return nil, err
		}
		gr.edge = map[string]string{"manual": EdgeAssigned, "request": EdgeRequested, "activation": EdgeActivated,
			"eligibility": EdgeEligible, "birthright": EdgeMatches}[origin]
		g.grants[ent] = append(g.grants[ent], gr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Accounts that hold them in the applications.
	rows, err = tx.Query(ctx, `
		SELECT ae.entitlement_id, a.id, coalesce(a.username, a.native_id), app.name, app.connector = $2,
		       coalesce(a.identity_id::text, ''), coalesce(i.display_name, ''), coalesce(i.status, ''), ae.expires_at
		FROM account_entitlements ae
		JOIN accounts a ON a.id = ae.account_id
		JOIN applications app ON app.id = a.application_id
		LEFT JOIN identities i ON i.id = a.identity_id
		WHERE ae.entitlement_id = ANY($1::uuid[]) AND a.status = 'active'
		  AND (ae.expires_at IS NULL OR ae.expires_at > now())
		ORDER BY coalesce(i.display_name, ''), 3, a.id`, keys, authz.BuiltinConnector)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ent string
		var h holding
		if err := rows.Scan(&ent, &h.accountID, &h.account, &h.app, &h.builtin, &h.owner, &h.ownerName, &h.ownerStatus, &h.until); err != nil {
			rows.Close()
			return nil, err
		}
		h.ownerActive = h.ownerStatus == "active"
		g.holdings[ent] = append(g.holdings[ent], h)
	}
	rows.Close()
	return g, rows.Err()
}

// paths are every way to the entitlement id, going through at most depth
// more credentials. seen are the entitlements already on the way, so a
// credential that leads back to itself ends there.
func (g *Graph) paths(id string, depth int, seen map[string]bool) []Path {
	if p, ok := g.memo[id]; ok && depth == maxDepth {
		return p
	}
	e := g.ents[id]
	if e == nil {
		return nil
	}
	seen[id] = true
	defer delete(seen, id)
	end := Step{Kind: KindEntitlement, ID: e.id, Name: e.name, Detail: e.app}

	var out []Path
	given := map[string]bool{} // people Grantra gave it to
	for _, gr := range g.grants[id] {
		p := Path{IdentityID: gr.identity, IdentityName: gr.name, Until: gr.until,
			Standing: gr.until == nil && gr.edge != EdgeEligible,
			Steps:    []Step{{Kind: KindPerson, ID: gr.identity, Name: gr.name}}}
		edge := gr.edge
		if gr.ruleID != "" {
			p.Steps = append(p.Steps, Step{Kind: KindRule, ID: gr.ruleID, Name: gr.ruleName, Edge: EdgeMatches})
			edge = EdgeGives
		}
		if gr.roleID != "" {
			p.Steps = append(p.Steps, Step{Kind: KindRole, ID: gr.roleID, Name: gr.roleName, Edge: edge})
			edge = EdgeIncludes
		}
		end.Edge = edge
		p.Steps = append(p.Steps, end)
		out = append(out, p)
		// Someone only eligible whose account holds it anyway holds it for
		// good; that path stays.
		if gr.edge != EdgeEligible {
			given[gr.identity] = true
		}
	}
	for _, h := range g.holdings[id] {
		if len(g.controlled[h.accountID]) > 0 || !h.ownerActive {
			continue
		}
		// An account Grantra provisioned holds what an assignment gave; it
		// is the same path. Grantra's own accounts are how it gives
		// platform roles.
		if given[h.owner] && !h.builtin {
			continue
		}
		end.Edge = EdgeHolds
		out = append(out, Path{IdentityID: h.owner, IdentityName: h.ownerName, Until: h.until,
			Standing: h.until == nil, Outside: !h.builtin,
			Steps: []Step{{Kind: KindPerson, ID: h.owner, Name: h.ownerName},
				{Kind: KindAccount, ID: h.accountID, Name: h.account, Detail: h.app, Edge: EdgeOwns}, end}})
	}
	if depth > 0 {
		for _, h := range g.holdings[id] {
			for _, c := range g.controlled[h.accountID] {
				if seen[c.ent] {
					continue
				}
				for _, via := range g.paths(c.ent, depth-1, seen) {
					out = append(out, through(via, c, h, end))
				}
			}
		}
	}
	if depth == maxDepth {
		g.memo[id] = out
	}
	return out
}

// through extends a path to a credential's entitlement on to the target:
// the credential instead of its entitlement, the account it logs in as,
// and what the account holds.
func through(via Path, c control, h holding, end Step) Path {
	last := via.Steps[len(via.Steps)-1]
	steps := append(slices.Clone(via.Steps[:len(via.Steps)-1]),
		Step{Kind: c.kind, ID: c.id, Name: c.name, Edge: last.Edge},
		Step{Kind: KindAccount, ID: h.accountID, Name: h.account, Detail: h.app, Edge: EdgeLogsInAs})
	end.Edge = EdgeHolds
	p := via
	p.Steps = append(steps, end)
	p.Credential = true
	p.Standing = via.Standing && h.until == nil
	if h.until != nil && (p.Until == nil || h.until.Before(*p.Until)) {
		p.Until = h.until
	}
	return p
}

func (g *Graph) all(id string) []Path { return g.paths(id, maxDepth, map[string]bool{}) }

// orphans are the active accounts that hold the entitlement id and belong
// to no active person, credentials Grantra hands out aside.
func (g *Graph) orphans(id string) []Orphan {
	var out []Orphan
	for _, h := range g.holdings[id] {
		if h.ownerActive || len(g.controlled[h.accountID]) > 0 {
			continue
		}
		out = append(out, Orphan{AccountID: h.accountID, Account: h.account, Application: h.app,
			Owner: h.ownerName, OwnerStatus: h.ownerStatus})
	}
	return out
}

// summary counts who reaches e by paths.
func summary(e *entitlement, paths []Path, orphans int) Target {
	t := Target{ID: e.id, Name: e.name, Application: e.app, Risk: e.risk, JITOnly: e.jitOnly, Orphans: orphans}
	count := func(keep func(Path) bool) int {
		people := map[string]bool{}
		for _, p := range paths {
			if keep(p) {
				people[p.IdentityID] = true
			}
		}
		return len(people)
	}
	t.People = count(func(Path) bool { return true })
	t.Standing = count(func(p Path) bool { return p.Standing })
	t.Credential = count(func(p Path) bool { return p.Credential })
	t.Outside = count(func(p Path) bool { return p.Outside })
	return t
}

// riskier orders targets: critical first, then the most people standing,
// then the most people.
func riskier(a, b Target) int {
	return cmp.Or(cmp.Compare(rank(b.Risk), rank(a.Risk)), cmp.Compare(b.Standing+b.Credential+b.Outside, a.Standing+a.Credential+a.Outside),
		cmp.Compare(b.People, a.People), cmp.Compare(a.Application, b.Application), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}

func rank(risk string) int { return slices.Index(catalog.Risks, risk) }

// Targets lists the targets someone reaches or an orphan holds, riskiest
// first.
func (g *Graph) Targets() []Target {
	var out []Target
	for id, e := range g.ents {
		if !e.target {
			continue
		}
		t := summary(e, g.all(id), len(g.orphans(id)))
		if t.People > 0 || t.Orphans > 0 {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, riskier)
	return out
}

// Detail is one target and every way to it.
func (g *Graph) Detail(id string) (Detail, error) {
	e := g.ents[id]
	if !db.ValidUUID(id) || e == nil || !e.target {
		return Detail{}, apperr.ErrNotFound
	}
	paths := g.all(id)
	d := Detail{OrphanAccounts: g.orphans(id)}
	d.Target = summary(e, paths, len(d.OrphanAccounts))
	slices.SortStableFunc(paths, func(a, b Path) int {
		return cmp.Or(cmp.Compare(a.IdentityName, b.IdentityName), cmp.Compare(a.IdentityID, b.IdentityID))
	})
	if len(paths) > MaxPaths {
		paths, d.Truncated = paths[:MaxPaths], true
	}
	d.Paths = paths
	if d.OrphanAccounts == nil {
		d.OrphanAccounts = []Orphan{}
	}
	return d, nil
}

// Reach lists the targets the identity reaches, riskiest first: what
// whoever takes over their account gets.
func (g *Graph) Reach(identity string) []Reach {
	var out []Reach
	for id, e := range g.ents {
		if !e.target {
			continue
		}
		var mine []Path
		for _, p := range g.all(id) {
			if p.IdentityID == identity {
				mine = append(mine, p)
			}
		}
		if len(mine) > 0 {
			out = append(out, Reach{Target: summary(e, mine, 0), Paths: mine})
		}
	}
	slices.SortFunc(out, func(a, b Reach) int { return riskier(a.Target, b.Target) })
	return out
}
