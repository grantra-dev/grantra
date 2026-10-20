package insights

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
)

// Role mining looks, in every department, and every job title within one,
// for access most people there hold one by one: what a business role and a
// birthright rule could give them all instead.
// The thresholds are the tenant's (see limits).
//
// maxSuggestions keeps the list to what anyone reads.
const maxSuggestions = 50

// RoleSuggestion is a business role Grantra found in what people hold.
type RoleSuggestion struct {
	// Name is a name for the role: the department, and the title.
	Name string `json:"name"`
	// Conditions are who the group is, as a birthright rule would say it:
	// {"department": ["Sales"]}, with "title" for a title in it.
	Conditions map[string][]string `json:"conditions"`
	// People is how many active people are in the group, and HaveAll how
	// many of them hold every entitlement already.
	People       int                    `json:"people"`
	HaveAll      int                    `json:"have_all"`
	Entitlements []SuggestedEntitlement `json:"entitlements"`
}

// SuggestedEntitlement is one entitlement of a suggested role, and how many
// of the group hold it.
type SuggestedEntitlement struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Application string `json:"application"`
	Risk        string `json:"risk"`
	Holders     int    `json:"holders"`
}

// person is someone in a group, and the entitlements they hold one by one.
type person struct {
	department, title string
	holds             map[string]bool
}

// SuggestRoles finds business roles in what active people hold one by one:
// by hand, by request, or in an application without Grantra giving it.
// What a role or a birthright rule already gives is left out, and so are
// just-in-time only entitlements (a role would make them standing) and
// Grantra's own platform roles.
func SuggestRoles(ctx context.Context, tx pgx.Tx) ([]RoleSuggestion, error) {
	l, err := loadLimits(ctx, tx)
	if err != nil {
		return nil, err
	}
	people := map[string]*person{}
	rows, err := tx.Query(ctx, `
		SELECT i.id, trim(i.attributes ->> 'department'), trim(coalesce(i.attributes ->> 'title', ''))
		FROM identities i
		WHERE i.status = 'active' AND i.kind = 'human' AND trim(coalesce(i.attributes ->> 'department', '')) <> ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		p := &person{holds: map[string]bool{}}
		if err := rows.Scan(&id, &p.department, &p.title); err != nil {
			rows.Close()
			return nil, err
		}
		people[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		WITH held AS (
			SELECT x.identity_id, x.entitlement_id FROM access_assignments x
			WHERE x.entitlement_id IS NOT NULL AND x.origin IN ('manual', 'request') AND x.expires_at IS NULL
			UNION
			SELECT a.identity_id, ae.entitlement_id FROM accounts a JOIN account_entitlements ae ON ae.account_id = a.id
			WHERE a.identity_id IS NOT NULL AND a.status = 'active' AND ae.expires_at IS NULL
		)
		SELECT h.identity_id, h.entitlement_id
		FROM held h
		JOIN identities i ON i.id = h.identity_id
		JOIN entitlements e ON e.id = h.entitlement_id
		JOIN applications app ON app.id = e.application_id
		WHERE i.status = 'active' AND i.kind = 'human' AND trim(coalesce(i.attributes ->> 'department', '')) <> ''
		  AND NOT e.jit_only AND app.connector <> $1
		  AND NOT EXISTS (SELECT 1 FROM access_assignments y
		                  WHERE y.identity_id = h.identity_id AND (y.expires_at IS NULL OR y.expires_at > now())
		                    AND (y.origin = 'birthright' OR y.role_id IS NOT NULL)
		                    AND (y.entitlement_id = h.entitlement_id
		                         OR y.role_id IN (SELECT role_id FROM role_entitlements WHERE entitlement_id = h.entitlement_id)))`,
		authz.BuiltinConnector)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, ent string
		if err := rows.Scan(&id, &ent); err != nil {
			rows.Close()
			return nil, err
		}
		if p := people[id]; p != nil {
			p.holds[ent] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Departments first; then the titles within one, for what goes beyond
	// what the whole department would get. Groups ignore case, as rules
	// do; the first spelling met names them.
	type group struct {
		department, title string
		members           []*person
	}
	groups := map[[2]string]*group{}
	key := func(dept, title string) [2]string { return [2]string{strings.ToLower(dept), strings.ToLower(title)} }
	add := func(dept, title string, p *person) {
		k := key(dept, title)
		if groups[k] == nil {
			groups[k] = &group{department: dept, title: title}
		}
		groups[k].members = append(groups[k].members, p)
	}
	for _, p := range people {
		add(p.department, "", p)
		if p.title != "" {
			add(p.department, p.title, p)
		}
	}
	shared := func(g *group) map[string]int {
		counts := map[string]int{}
		for _, p := range g.members {
			for e := range p.holds {
				counts[e]++
			}
		}
		out := map[string]int{}
		for e, n := range counts {
			if n*100 >= l.roleMinShare*len(g.members) {
				out[e] = n
			}
		}
		return out
	}
	var out []RoleSuggestion
	var ents []string
	for _, g := range groups {
		if len(g.members) < l.roleMinPeople {
			continue
		}
		picked := shared(g)
		if g.title != "" {
			if dept := groups[key(g.department, "")]; dept != nil && len(dept.members) >= l.roleMinPeople {
				for e := range shared(dept) {
					delete(picked, e)
				}
			}
		}
		if len(picked) < l.roleMinEntitlements {
			continue
		}
		s := RoleSuggestion{Name: g.department, Conditions: map[string][]string{"department": {g.department}},
			People: len(g.members)}
		if g.title != "" {
			s.Name += " · " + g.title
			s.Conditions["title"] = []string{g.title}
		}
		for _, p := range g.members {
			all := true
			for e := range picked {
				all = all && p.holds[e]
			}
			if all {
				s.HaveAll++
			}
		}
		for e, n := range picked {
			s.Entitlements = append(s.Entitlements, SuggestedEntitlement{ID: e, Holders: n})
			ents = append(ents, e)
		}
		out = append(out, s)
	}
	// The groups where a role would replace the most grants, first.
	slices.SortFunc(out, func(a, b RoleSuggestion) int {
		return cmp.Or(cmp.Compare(b.People*len(b.Entitlements), a.People*len(a.Entitlements)), cmp.Compare(a.Name, b.Name))
	})
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	if err := describe(ctx, tx, out, ents); err != nil {
		return nil, err
	}
	return out, nil
}

// describe fills in the names of the suggested entitlements, most held
// first.
func describe(ctx context.Context, tx pgx.Tx, out []RoleSuggestion, ids []string) error {
	if len(out) == 0 {
		return nil
	}
	type info struct{ name, app, risk string }
	infos := map[string]info{}
	rows, err := tx.Query(ctx, `SELECT e.id, e.name, app.name, e.risk FROM entitlements e
		JOIN applications app ON app.id = e.application_id WHERE e.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var in info
		if err := rows.Scan(&id, &in.name, &in.app, &in.risk); err != nil {
			rows.Close()
			return err
		}
		infos[id] = in
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		for j, e := range out[i].Entitlements {
			in := infos[e.ID]
			out[i].Entitlements[j].Name, out[i].Entitlements[j].Application, out[i].Entitlements[j].Risk = in.name, in.app, in.risk
		}
		slices.SortFunc(out[i].Entitlements, func(a, b SuggestedEntitlement) int {
			return cmp.Or(cmp.Compare(b.Holders, a.Holders), cmp.Compare(a.Application, b.Application), cmp.Compare(a.Name, b.Name))
		})
	}
	return nil
}
