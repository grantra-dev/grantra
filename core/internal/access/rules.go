// Package access decides what access each identity should have, and why.
//
// An assignment gives an identity a business role or an entitlement for a
// reason: a birthright rule, a manual grant or an approved request. This
// package owns birthright rules ("everyone in Sales gets the CRM") and keeps
// their assignments in step with people's attributes: Reconcile adds what a
// rule now gives and removes what it no longer does, and a leaver loses every
// assignment. Lifecycle events from HR syncs tell it whom to look at.
package access

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Conditions say who a rule applies to: each attribute must take one of its
// values, compared ignoring case and surrounding spaces. "kind" matches the
// identity's kind (human, service, agent, workload). No conditions match
// every person. Non-human identities only match rules that name their kind,
// so a rule for everyone never gives a service account an employee's access.
type Conditions map[string][]string

// Validate checks the conditions are usable.
func (c Conditions) Validate() error {
	if len(c) > 20 {
		return errors.New("a rule can have at most 20 conditions")
	}
	for attr, values := range c {
		if attr == "" || strings.TrimSpace(attr) != attr || len(attr) > 100 {
			return fmt.Errorf("condition attribute %q must be a non-empty name without surrounding spaces", attr)
		}
		if len(values) == 0 || len(values) > 200 {
			return fmt.Errorf("condition %q needs between 1 and 200 values", attr)
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("condition %q has an empty value", attr)
			}
		}
	}
	return nil
}

// Match reports whether an identity with these attributes and kind matches.
func (c Conditions) Match(attrs map[string]string, kind string) bool {
	if _, named := c["kind"]; !named && kind != "human" {
		return false
	}
	for attr, values := range c {
		have := attrs[attr]
		if attr == "kind" {
			have = kind
		}
		have = strings.TrimSpace(have)
		if !slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), have) }) {
			return false
		}
	}
	return true
}

// Ref names a role or entitlement a rule gives.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Application is set for entitlements.
	Application string `json:"application,omitempty"`
}

// Rule is a birthright rule.
type Rule struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Conditions   Conditions `json:"conditions"`
	Enabled      bool       `json:"enabled"`
	Roles        []Ref      `json:"roles"`
	Entitlements []Ref      `json:"entitlements"`
	// Holders is how many identities hold access through the rule right now.
	Holders   int       `json:"holders"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RuleInput describes a rule to create or replace.
type RuleInput struct {
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Conditions     Conditions `json:"conditions"`
	Enabled        *bool      `json:"enabled"`
	RoleIDs        []string   `json:"role_ids"`
	EntitlementIDs []string   `json:"entitlement_ids"`
}

func (in *RuleInput) check(ctx context.Context, tx pgx.Tx) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		return apperr.Invalidf("name must be between 1 and 200 characters")
	}
	if in.Conditions == nil {
		in.Conditions = Conditions{}
	}
	if err := in.Conditions.Validate(); err != nil {
		return &apperr.Invalid{Msg: err.Error()}
	}
	in.RoleIDs, in.EntitlementIDs = dedupe(in.RoleIDs), dedupe(in.EntitlementIDs)
	if len(in.RoleIDs)+len(in.EntitlementIDs) == 0 {
		return apperr.Invalidf("a rule must give at least one role or entitlement")
	}
	if err := catalog.CheckRoles(ctx, tx, in.RoleIDs); err != nil {
		return err
	}
	if err := catalog.CheckEntitlements(ctx, tx, in.EntitlementIDs); err != nil {
		return err
	}
	// Just-in-time only access (vault access among it) is never held
	// without an end date; a rule would hand it out for good to whoever
	// matches, the admin who wrote it included.
	var jit bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM entitlements WHERE id = ANY($1::uuid[]) AND jit_only)
		    OR EXISTS (SELECT 1 FROM unnest($2::uuid[]) r WHERE access_is_jit_only(r, NULL))`, in.EntitlementIDs, in.RoleIDs).Scan(&jit)
	if err != nil {
		return err
	}
	if jit {
		return apperr.Invalidf("birthright rules cannot give just-in-time only access: people are made eligible and activate it, or request it for a time")
	}
	return nil
}

// CreateRule adds a birthright rule. Call Reconcile afterwards to apply it.
func CreateRule(ctx context.Context, tx pgx.Tx, tenantID string, in RuleInput) (Rule, error) {
	return saveRule(ctx, tx, tenantID, "", in)
}

// UpdateRule replaces a rule. Call Reconcile afterwards to apply it.
func UpdateRule(ctx context.Context, tx pgx.Tx, tenantID, id string, in RuleInput) (Rule, error) {
	if !db.ValidUUID(id) {
		return Rule{}, apperr.ErrNotFound
	}
	return saveRule(ctx, tx, tenantID, id, in)
}

func saveRule(ctx context.Context, tx pgx.Tx, tenantID, id string, in RuleInput) (Rule, error) {
	if err := in.check(ctx, tx); err != nil {
		return Rule{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	desc := strings.TrimSpace(in.Description)
	var err error
	if id == "" {
		err = tx.QueryRow(ctx,
			`INSERT INTO access_rules (tenant_id, name, description, conditions, enabled)
			 VALUES ($1, $2, nullif($3, ''), $4, $5) RETURNING id`,
			tenantID, in.Name, desc, in.Conditions, enabled).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx,
			`UPDATE access_rules SET name = $2, description = nullif($3, ''), conditions = $4, enabled = $5, updated_at = now()
			 WHERE id = $1`, id, in.Name, desc, in.Conditions, enabled)
		if err == nil && tag.RowsAffected() == 0 {
			return Rule{}, apperr.ErrNotFound
		}
	}
	if db.IsUniqueViolation(err) {
		return Rule{}, apperr.Conflictf("a rule named %q already exists", in.Name)
	}
	if err != nil {
		return Rule{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM access_rule_grants WHERE rule_id = $1`, id); err != nil {
		return Rule{}, err
	}
	for _, r := range in.RoleIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO access_rule_grants (tenant_id, rule_id, role_id) VALUES ($1, $2, $3)`, tenantID, id, r); err != nil {
			return Rule{}, err
		}
	}
	for _, e := range in.EntitlementIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO access_rule_grants (tenant_id, rule_id, entitlement_id) VALUES ($1, $2, $3)`, tenantID, id, e); err != nil {
			return Rule{}, err
		}
	}
	return GetRule(ctx, tx, id)
}

// GetRule returns one rule.
func GetRule(ctx context.Context, tx pgx.Tx, id string) (Rule, error) {
	if !db.ValidUUID(id) {
		return Rule{}, apperr.ErrNotFound
	}
	rules, err := listRules(ctx, tx, id)
	if err != nil {
		return Rule{}, err
	}
	if len(rules) == 0 {
		return Rule{}, apperr.ErrNotFound
	}
	return rules[0], nil
}

// ListRules returns every rule.
func ListRules(ctx context.Context, tx pgx.Tx) ([]Rule, error) { return listRules(ctx, tx, "") }

func listRules(ctx context.Context, tx pgx.Tx, id string) ([]Rule, error) {
	rows, err := tx.Query(ctx,
		`SELECT r.id, r.name, coalesce(r.description, ''), r.conditions, r.enabled, r.created_at, r.updated_at,
		        (SELECT count(DISTINCT identity_id) FROM access_assignments a WHERE a.rule_id = r.id)
		 FROM access_rules r WHERE $1 = '' OR r.id = $1::uuid ORDER BY r.name, r.id`, id)
	if err != nil {
		return nil, err
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Rule, error) {
		r := Rule{Roles: []Ref{}, Entitlements: []Ref{}}
		err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Conditions, &r.Enabled, &r.CreatedAt, &r.UpdatedAt, &r.Holders)
		return r, err
	})
	if err != nil || len(rules) == 0 {
		return rules, err
	}
	byID := map[string]int{}
	for i, r := range rules {
		byID[r.ID] = i
	}
	rows, err = tx.Query(ctx,
		`SELECT g.rule_id, coalesce(g.role_id, g.entitlement_id), g.role_id IS NOT NULL,
		        coalesce(ro.name, e.name), coalesce(a.name, '')
		 FROM access_rule_grants g
		 LEFT JOIN roles ro ON ro.id = g.role_id
		 LEFT JOIN entitlements e ON e.id = g.entitlement_id
		 LEFT JOIN applications a ON a.id = e.application_id
		 WHERE $1 = '' OR g.rule_id = $1::uuid
		 ORDER BY 4, 2`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			ruleID string
			isRole bool
			ref    Ref
		)
		if err := rows.Scan(&ruleID, &ref.ID, &isRole, &ref.Name, &ref.Application); err != nil {
			return nil, err
		}
		i, ok := byID[ruleID]
		if !ok {
			continue
		}
		if isRole {
			rules[i].Roles = append(rules[i].Roles, ref)
		} else {
			rules[i].Entitlements = append(rules[i].Entitlements, ref)
		}
	}
	return rules, rows.Err()
}

// Match is an identity a set of conditions selects.
type Match struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// Preview returns how many identities that can hold access (not terminated)
// match the conditions, and up to limit of them.
func Preview(ctx context.Context, tx pgx.Tx, conds Conditions, limit int) (int, []Match, error) {
	if err := conds.Validate(); err != nil {
		return 0, nil, &apperr.Invalid{Msg: err.Error()}
	}
	people, err := loadPeople(ctx, tx, nil)
	if err != nil {
		return 0, nil, err
	}
	n, sample := 0, []Match{}
	for _, p := range people {
		if p.Status == "terminated" || !conds.Match(p.Attributes, p.Kind) {
			continue
		}
		n++
		if len(sample) < limit {
			sample = append(sample, Match{ID: p.ID, DisplayName: p.DisplayName, Status: p.Status})
		}
	}
	return n, sample, nil
}

// Attribute is an HR attribute people have, with the values most of them
// take, to help write conditions.
type Attribute struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Attributes lists the attributes of identities that are not terminated,
// with up to limit of their most common values each, plus "kind".
func Attributes(ctx context.Context, tx pgx.Tx, limit int) ([]Attribute, error) {
	rows, err := tx.Query(ctx, `
		WITH v AS (
		  SELECT a.key, btrim(a.value) AS value, count(*) AS n
		  FROM identities i, jsonb_each_text(i.attributes) a
		  WHERE i.status <> 'terminated' AND btrim(a.value) <> ''
		  GROUP BY 1, 2
		  UNION ALL
		  SELECT 'kind', kind, count(*) FROM identities WHERE status <> 'terminated' GROUP BY 2),
		ranked AS (SELECT key, value, row_number() OVER (PARTITION BY key ORDER BY n DESC, value) AS r FROM v)
		SELECT key, array_agg(value ORDER BY r) FROM ranked WHERE r <= $1 GROUP BY key ORDER BY key`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attribute, error) {
		var a Attribute
		err := row.Scan(&a.Name, &a.Values)
		return a, err
	})
}

func dedupe(ids []string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return slices.Sorted(maps.Keys(set))
}
