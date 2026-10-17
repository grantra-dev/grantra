package access

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Origins of an assignment.
const (
	OriginBirthright = "birthright"
	OriginManual     = "manual"
	OriginRequest    = "request"
)

// Assignment is a role or entitlement an identity should have, and why.
type Assignment struct {
	ID string `json:"id"`
	// Type is "role" or "entitlement".
	Type        string     `json:"type"`
	TargetID    string     `json:"target_id"`
	Name        string     `json:"name"`
	Application string     `json:"application,omitempty"`
	Origin      string     `json:"origin"`
	RuleID      string     `json:"rule_id,omitempty"`
	RuleName    string     `json:"rule_name,omitempty"`
	GrantedBy   string     `json:"granted_by"`
	GrantedAt   time.Time  `json:"granted_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// Justification is why an activation was made.
	Justification string `json:"justification,omitempty"`
}

// IdentityAccess lists the assignments of one identity.
func IdentityAccess(ctx context.Context, tx pgx.Tx, identityID string) ([]Assignment, error) {
	rows, err := tx.Query(ctx,
		`SELECT x.id, CASE WHEN x.role_id IS NULL THEN 'entitlement' ELSE 'role' END,
		        coalesce(x.role_id, x.entitlement_id), coalesce(ro.name, e.name), coalesce(app.name, ''),
		        x.origin, coalesce(x.rule_id::text, ''), coalesce(r.name, ''), x.granted_by, x.granted_at, x.expires_at,
		        coalesce(x.justification, '')
		 FROM access_assignments x
		 LEFT JOIN roles ro ON ro.id = x.role_id
		 LEFT JOIN entitlements e ON e.id = x.entitlement_id
		 LEFT JOIN applications app ON app.id = e.application_id
		 LEFT JOIN access_rules r ON r.id = x.rule_id
		 WHERE x.identity_id = $1
		 ORDER BY 4, 3, x.origin, 8`, identityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Assignment, error) {
		var a Assignment
		err := row.Scan(&a.ID, &a.Type, &a.TargetID, &a.Name, &a.Application, &a.Origin, &a.RuleID, &a.RuleName,
			&a.GrantedBy, &a.GrantedAt, &a.ExpiresAt, &a.Justification)
		return a, err
	})
}

// Changes counts what a reconcile did.
type Changes struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

type person struct {
	ID, DisplayName, Status, Kind string
	Attributes                    map[string]string
}

func loadPeople(ctx context.Context, tx pgx.Tx, ids []string) ([]person, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, display_name, status, kind, attributes FROM identities
		 WHERE $1::uuid[] IS NULL OR id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (person, error) {
		var (
			p     person
			attrs map[string]any
		)
		err := row.Scan(&p.ID, &p.DisplayName, &p.Status, &p.Kind, &attrs)
		p.Attributes = make(map[string]string, len(attrs))
		for k, v := range attrs {
			if s, ok := v.(string); ok {
				p.Attributes[k] = s
			} else {
				b, _ := json.Marshal(v)
				p.Attributes[k] = string(b)
			}
		}
		return p, err
	})
}

type grant struct {
	role, entitlement string // exactly one is set
}

type activeRule struct {
	id, name   string
	conditions Conditions
	grants     []grant
}

func loadActiveRules(ctx context.Context, tx pgx.Tx) ([]activeRule, error) {
	rows, err := tx.Query(ctx,
		`SELECT r.id, r.name, r.conditions, coalesce(g.role_id::text, ''), coalesce(g.entitlement_id::text, '')
		 FROM access_rules r JOIN access_rule_grants g ON g.rule_id = r.id
		 WHERE r.enabled ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	var rules []activeRule
	for rows.Next() {
		var (
			id, name string
			conds    Conditions
			g        grant
		)
		if err := rows.Scan(&id, &name, &conds, &g.role, &g.entitlement); err != nil {
			return nil, err
		}
		if len(rules) == 0 || rules[len(rules)-1].id != id {
			rules = append(rules, activeRule{id: id, name: name, conditions: conds})
		}
		rules[len(rules)-1].grants = append(rules[len(rules)-1].grants, g)
	}
	return rules, rows.Err()
}

type held struct {
	id, identity, role, entitlement, origin, rule string
}

func (h held) key() string { return h.identity + "|" + h.role + h.entitlement + "|" + h.rule }

// Reconcile brings the assignments of the given identities (every identity
// when ids is nil) in line with the birthright rules: it adds what a rule now
// gives and removes birthright access a rule no longer gives. Terminated
// identities lose every assignment, whatever its origin. Suspended identities
// keep theirs. Each change is audited with actor.
func Reconcile(ctx context.Context, tx pgx.Tx, tenantID, actor string, ids []string) (Changes, error) {
	var ch Changes
	if ids != nil && len(ids) == 0 {
		return ch, nil
	}
	people, err := loadPeople(ctx, tx, ids)
	if err != nil {
		return ch, err
	}
	rules, err := loadActiveRules(ctx, tx)
	if err != nil {
		return ch, err
	}
	rows, err := tx.Query(ctx,
		`SELECT id, identity_id, coalesce(role_id::text, ''), coalesce(entitlement_id::text, ''), origin, coalesce(rule_id::text, '')
		 FROM access_assignments WHERE $1::uuid[] IS NULL OR identity_id = ANY($1)
		 FOR UPDATE`, ids)
	if err != nil {
		return ch, err
	}
	current, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (held, error) {
		var h held
		err := row.Scan(&h.id, &h.identity, &h.role, &h.entitlement, &h.origin, &h.rule)
		return h, err
	})
	if err != nil {
		return ch, err
	}

	ruleName := map[string]string{}
	for _, r := range rules {
		ruleName[r.id] = r.name
	}
	want := map[string]held{}
	terminated := map[string]bool{}
	for _, p := range people {
		if p.Status == "terminated" {
			terminated[p.ID] = true
			continue
		}
		for _, r := range rules {
			if !r.conditions.Match(p.Attributes, p.Kind) {
				continue
			}
			for _, g := range r.grants {
				h := held{identity: p.ID, role: g.role, entitlement: g.entitlement, origin: OriginBirthright, rule: r.id}
				want[h.key()] = h
			}
		}
	}

	have := map[string]bool{}
	var gone []removal
	for _, h := range current {
		switch {
		case terminated[h.identity]:
			gone = append(gone, removal{h, "identity terminated"})
		case h.origin != OriginBirthright:
		case !wanted(want, h):
			gone = append(gone, removal{h, "no longer matches the rule"})
		default:
			have[h.key()] = true
		}
	}
	if err := remove(ctx, tx, tenantID, actor, gone); err != nil {
		return ch, err
	}
	ch.Removed = len(gone)
	if err := removeEligibilities(ctx, tx, tenantID, actor, terminated); err != nil {
		return ch, err
	}
	var missing []held
	for k, h := range want {
		if !have[k] {
			missing = append(missing, h)
		}
	}
	ch.Added, err = add(ctx, tx, tenantID, actor, missing, ruleName)
	return ch, err
}

// removeEligibilities takes leavers' eligibilities away, so nothing comes
// back if they return.
func removeEligibilities(ctx context.Context, tx pgx.Tx, tenantID, actor string, terminated map[string]bool) error {
	if len(terminated) == 0 {
		return nil
	}
	ids := make([]string, 0, len(terminated))
	for id := range terminated {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx,
		`DELETE FROM access_eligibilities WHERE identity_id = ANY($1::uuid[])
		 RETURNING id::text, identity_id::text, coalesce(role_id::text, ''), coalesce(entitlement_id::text, '')`, ids)
	if err != nil {
		return err
	}
	type gone struct {
		id, identity string
		t            Target
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (gone, error) {
		var g gone
		err := row.Scan(&g.id, &g.identity, &g.t.RoleID, &g.t.EntitlementID)
		return g, err
	})
	if err != nil {
		return err
	}
	return db.SendBatch(ctx, tx, list, func(b *pgx.Batch, g gone) {
		db.QueueAudit(b, tenantID, actor, "access.eligibility_removed", "identity", g.identity,
			g.t.details(map[string]any{"eligibility_id": g.id, "reason": "identity terminated"}))
	}, func(br pgx.BatchResults, _ gone) error {
		return db.ReadAudit(br, "access.eligibility_removed")
	})
}

func wanted(want map[string]held, h held) bool {
	_, ok := want[h.key()]
	return ok
}

func target(h held) (string, string) {
	if h.role != "" {
		return "role_id", h.role
	}
	return "entitlement_id", h.entitlement
}

// add gives the access in list that its people do not have yet, with an
// audit event each, and says how many it gave.
func add(ctx context.Context, tx pgx.Tx, tenantID, actor string, list []held, ruleName map[string]string) (int, error) {
	added := 0
	err := db.SendBatch(ctx, tx, list, func(b *pgx.Batch, h held) {
		var role, ent *string
		if h.role != "" {
			role = &h.role
		} else {
			ent = &h.entitlement
		}
		field, value := target(h)
		details := map[string]any{field: value, "origin": h.origin, "rule_id": h.rule, "rule": ruleName[h.rule]}
		b.Queue(`WITH a AS (
			   INSERT INTO access_assignments (tenant_id, identity_id, role_id, entitlement_id, origin, rule_id, granted_by)
			   VALUES ($1, $2, $3, $4, $5, $6, $7)
			   ON CONFLICT DO NOTHING RETURNING id),
			 audit AS (
			   INSERT INTO audit_events (tenant_id, actor, action, target_type, target_id, details)
			   SELECT $1, $7, 'access.assigned', 'identity', $2::text, $8::jsonb || jsonb_build_object('assignment_id', a.id) FROM a)
			 SELECT count(*) FROM a`,
			tenantID, h.identity, role, ent, h.origin, h.rule, actor, details)
	}, func(br pgx.BatchResults, _ held) error {
		var n int
		if err := br.QueryRow().Scan(&n); err != nil {
			return fmt.Errorf("assign: %w", err)
		}
		added += n
		return nil
	})
	return added, err
}

// removal is access to take away, and why.
type removal struct {
	h      held
	reason string
}

// remove takes away access, with an audit event each.
func remove(ctx context.Context, tx pgx.Tx, tenantID, actor string, list []removal) error {
	return db.SendBatch(ctx, tx, list, func(b *pgx.Batch, r removal) {
		b.Queue(`DELETE FROM access_assignments WHERE id = $1`, r.h.id)
		field, value := target(r.h)
		details := map[string]any{"assignment_id": r.h.id, field: value, "origin": r.h.origin, "reason": r.reason}
		if r.h.rule != "" {
			details["rule_id"] = r.h.rule
		}
		db.QueueAudit(b, tenantID, actor, "access.removed", "identity", r.h.identity, details)
	}, func(br pgx.BatchResults, _ removal) error {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("unassign: %w", err)
		}
		return db.ReadAudit(br, "access.removed")
	})
}

// DeleteRule removes a rule and, with an audit event each, the access it gave.
func DeleteRule(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) (Changes, error) {
	var ch Changes
	if !db.ValidUUID(id) {
		return ch, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx,
		`SELECT id, identity_id, coalesce(role_id::text, ''), coalesce(entitlement_id::text, ''), origin, rule_id::text
		 FROM access_assignments WHERE rule_id = $1 FOR UPDATE`, id)
	if err != nil {
		return ch, err
	}
	given, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (held, error) {
		var h held
		err := row.Scan(&h.id, &h.identity, &h.role, &h.entitlement, &h.origin, &h.rule)
		return h, err
	})
	if err != nil {
		return ch, err
	}
	gone := make([]removal, len(given))
	for i, h := range given {
		gone[i] = removal{h, "rule deleted"}
	}
	if err := remove(ctx, tx, tenantID, actor, gone); err != nil {
		return ch, err
	}
	ch.Removed = len(gone)
	tag, err := tx.Exec(ctx, `DELETE FROM access_rules WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ch, apperr.ErrNotFound
	}
	return ch, err
}

// ProcessLifecycle reconciles the identities named by lifecycle events that
// nothing has processed yet, and marks those events processed. It runs in
// batches, each in its own transaction, until the outbox is empty. Several
// servers can run it at once: a batch skips events another one holds.
func ProcessLifecycle(ctx context.Context, pool *pgxpool.Pool, tenantID string) (events int, total Changes, err error) {
	for {
		var (
			n  int
			ch Changes
		)
		err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx,
				`SELECT id, identity_id FROM lifecycle_events WHERE processed_at IS NULL
				 ORDER BY id LIMIT 500 FOR UPDATE SKIP LOCKED`)
			if err != nil {
				return err
			}
			var (
				eventIDs []int64
				ids      []string
				seen     = map[string]bool{}
			)
			for rows.Next() {
				var (
					eid int64
					iid string
				)
				if err := rows.Scan(&eid, &iid); err != nil {
					return err
				}
				eventIDs = append(eventIDs, eid)
				if !seen[iid] {
					seen[iid] = true
					ids = append(ids, iid)
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			n = len(eventIDs)
			if n == 0 {
				return nil
			}
			ch, err = Reconcile(ctx, tx, tenantID, "system:birthright", ids)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE lifecycle_events SET processed_at = now() WHERE id = ANY($1)`, eventIDs)
			return err
		})
		if err != nil || n == 0 {
			return events, total, err
		}
		events += n
		total.Added += ch.Added
		total.Removed += ch.Removed
	}
}
