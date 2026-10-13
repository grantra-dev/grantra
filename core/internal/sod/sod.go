// Package sod enforces separation of duties. A policy names two sides, each
// a set of business roles and entitlements, that one person must not hold
// together: whoever holds something from side A and something from side B
// violates it.
//
// What someone holds counts both ways: their assignments (an entitlement
// directly or through a role), and what their accounts actually have in the
// applications, so access granted outside Grantra is caught too.
//
// Scans record violations and resolve the ones that no longer apply. Admins
// can accept a violation as an exception, with a reason and optionally an
// end date. Policies in "prevent" mode also refuse access requests that would
// create a violation.
package sod

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Modes.
const (
	Detect  = "detect"
	Prevent = "prevent"
)

// Violation statuses.
const (
	Open     = "open"
	Excepted = "excepted"
	Resolved = "resolved"
)

// Ref is a business role or an entitlement on one side of a policy.
type Ref struct {
	RoleID        string `json:"role_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
	// Name is filled in when reading.
	Name string `json:"name,omitempty"`
}

func (r Ref) id() string { return r.RoleID + r.EntitlementID }

// Policy is a separation of duties rule.
type Policy struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Severity    string    `json:"severity"`
	Mode        string    `json:"mode"`
	Enabled     bool      `json:"enabled"`
	SideA       []Ref     `json:"side_a"`
	SideB       []Ref     `json:"side_b"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Input describes a policy to save.
type Input struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Mode        string `json:"mode"`
	Enabled     *bool  `json:"enabled"`
	SideA       []Ref  `json:"side_a"`
	SideB       []Ref  `json:"side_b"`
}

func (in *Input) check(ctx context.Context, tx pgx.Tx) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		return apperr.Invalidf("name is required, up to 200 characters")
	}
	switch in.Severity {
	case "":
		in.Severity = "high"
	case "low", "medium", "high", "critical":
	default:
		return apperr.Invalidf("severity must be low, medium, high or critical")
	}
	switch in.Mode {
	case "":
		in.Mode = Detect
	case Detect, Prevent:
	default:
		return apperr.Invalidf(`mode must be "detect" or "prevent"`)
	}
	if len(in.SideA) == 0 || len(in.SideB) == 0 {
		return apperr.Invalidf("both side_a and side_b need at least one role or entitlement")
	}
	seen := map[string]string{}
	for side, refs := range map[string][]Ref{"side_a": in.SideA, "side_b": in.SideB} {
		for _, r := range refs {
			if (r.RoleID == "") == (r.EntitlementID == "") {
				return apperr.Invalidf("each entry of %s needs exactly one of role_id and entitlement_id", side)
			}
			if other, dup := seen[r.id()]; dup && other != side {
				return apperr.Invalidf("%s is on both sides", r.id())
			}
			seen[r.id()] = side
			table := "roles"
			if r.EntitlementID != "" {
				table = "entitlements"
			}
			var ok bool
			if db.ValidUUID(r.id()) {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, r.id()).Scan(&ok); err != nil {
					return err
				}
			}
			if !ok {
				return apperr.Invalidf("%s %s does not exist", strings.TrimSuffix(table, "s"), r.id())
			}
		}
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Save creates a policy (id empty) or replaces one. Violations follow at
// the next scan.
func Save(ctx context.Context, tx pgx.Tx, tenantID, id string, in Input) (Policy, error) {
	if err := in.check(ctx, tx); err != nil {
		return Policy{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	var err error
	if id == "" {
		err = tx.QueryRow(ctx,
			`INSERT INTO sod_policies (tenant_id, name, description, severity, mode, enabled)
			 VALUES ($1, $2, nullif($3, ''), $4, $5, $6) RETURNING id`,
			tenantID, in.Name, in.Description, in.Severity, in.Mode, enabled).Scan(&id)
	} else {
		if !db.ValidUUID(id) {
			return Policy{}, apperr.ErrNotFound
		}
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx,
			`UPDATE sod_policies SET name = $2, description = nullif($3, ''), severity = $4, mode = $5, enabled = $6, updated_at = now()
			 WHERE id = $1`, id, in.Name, in.Description, in.Severity, in.Mode, enabled)
		if err == nil && tag.RowsAffected() == 0 {
			return Policy{}, apperr.ErrNotFound
		}
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM sod_policy_sides WHERE policy_id = $1`, id)
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Policy{}, apperr.Conflictf("a policy named %q already exists", in.Name)
	}
	if err != nil {
		return Policy{}, err
	}
	for side, refs := range map[string][]Ref{"a": in.SideA, "b": in.SideB} {
		for _, r := range refs {
			if _, err := tx.Exec(ctx,
				`INSERT INTO sod_policy_sides (tenant_id, policy_id, side, role_id, entitlement_id)
				 VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
				tenantID, id, side, nullable(r.RoleID), nullable(r.EntitlementID)); err != nil {
				return Policy{}, err
			}
		}
	}
	return Get(ctx, tx, id)
}

// Delete removes a policy and its violations.
func Delete(ctx context.Context, tx pgx.Tx, id string) error {
	if !db.ValidUUID(id) {
		return apperr.ErrNotFound
	}
	tag, err := tx.Exec(ctx, `DELETE FROM sod_policies WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return err
}

// Get returns one policy.
func Get(ctx context.Context, tx pgx.Tx, id string) (Policy, error) {
	if !db.ValidUUID(id) {
		return Policy{}, apperr.ErrNotFound
	}
	list, err := list(ctx, tx, id)
	if err != nil {
		return Policy{}, err
	}
	if len(list) == 0 {
		return Policy{}, apperr.ErrNotFound
	}
	return list[0], nil
}

// List returns every policy.
func List(ctx context.Context, tx pgx.Tx) ([]Policy, error) { return list(ctx, tx, "") }

func list(ctx context.Context, tx pgx.Tx, id string) ([]Policy, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, coalesce(description, ''), severity, mode, enabled, created_at, updated_at
		 FROM sod_policies WHERE $1 = '' OR id::text = $1 ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Policy, error) {
		p := Policy{SideA: []Ref{}, SideB: []Ref{}}
		err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Severity, &p.Mode, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
		return p, err
	})
	if err != nil || len(out) == 0 {
		return out, err
	}
	byID := map[string]*Policy{}
	ids := make([]string, len(out))
	for i := range out {
		byID[out[i].ID] = &out[i]
		ids[i] = out[i].ID
	}
	rows, err = tx.Query(ctx,
		`SELECT s.policy_id, s.side, coalesce(s.role_id::text, ''), coalesce(s.entitlement_id::text, ''),
		        coalesce(r.name, app.name || ': ' || e.name)
		 FROM sod_policy_sides s
		 LEFT JOIN roles r ON r.id = s.role_id
		 LEFT JOIN entitlements e ON e.id = s.entitlement_id
		 LEFT JOIN applications app ON app.id = e.application_id
		 WHERE s.policy_id = ANY($1) ORDER BY 5`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, side string
		var r Ref
		if err := rows.Scan(&pid, &side, &r.RoleID, &r.EntitlementID, &r.Name); err != nil {
			return nil, err
		}
		if side == "a" {
			byID[pid].SideA = append(byID[pid].SideA, r)
		} else {
			byID[pid].SideB = append(byID[pid].SideB, r)
		}
	}
	return out, rows.Err()
}

// held lists what people hold: assignments (roles, entitlements and the
// entitlements of their roles) and the actual memberships of their active
// accounts. %[1]s limits it to one identity when not NULL; %[2]s adds rows.
const held = `
	held AS (
	  SELECT x.identity_id, x.role_id, x.entitlement_id FROM access_assignments x
	  WHERE (%[1]s IS NULL OR x.identity_id = %[1]s) AND (x.expires_at IS NULL OR x.expires_at > now())
	  UNION
	  SELECT x.identity_id, NULL, re.entitlement_id FROM access_assignments x JOIN role_entitlements re ON re.role_id = x.role_id
	  WHERE (%[1]s IS NULL OR x.identity_id = %[1]s) AND (x.expires_at IS NULL OR x.expires_at > now())
	  UNION
	  SELECT a.identity_id, NULL, ae.entitlement_id FROM accounts a JOIN account_entitlements ae ON ae.account_id = a.id
	  WHERE a.identity_id IS NOT NULL AND a.status = 'active' AND (%[1]s IS NULL OR a.identity_id = %[1]s)
	    AND (ae.expires_at IS NULL OR ae.expires_at > now())
	  %[2]s
	)`

// conflicts finds (policy, identity) pairs holding both sides of an enabled
// policy. extra is more rows for held.
const conflicts = `
	SELECT DISTINCT p.id, ha.identity_id
	FROM sod_policies p
	JOIN sod_policy_sides sa ON sa.policy_id = p.id AND sa.side = 'a'
	JOIN held ha ON ha.role_id = sa.role_id OR ha.entitlement_id = sa.entitlement_id
	JOIN sod_policy_sides sb ON sb.policy_id = p.id AND sb.side = 'b'
	JOIN held hb ON hb.identity_id = ha.identity_id AND (hb.role_id = sb.role_id OR hb.entitlement_id = sb.entitlement_id)
	WHERE p.enabled`

// Check returns a Conflict error naming the policies Conflicts finds, or nil.
func Check(ctx context.Context, tx pgx.Tx, identityID, roleID, entitlementID string) error {
	names, err := Conflicts(ctx, tx, identityID, roleID, entitlementID)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return apperr.Conflictf("this access would break separation of duties: %s", strings.Join(names, ", "))
	}
	return nil
}

// Conflicts names the prevent-mode policies that giving identityID the role
// or entitlement would violate, leaving out violations already accepted as
// exceptions.
func Conflicts(ctx context.Context, tx pgx.Tx, identityID, roleID, entitlementID string) ([]string, error) {
	extra := `UNION SELECT $1::uuid, $2::uuid, $3::uuid
	          UNION SELECT $1::uuid, NULL, re.entitlement_id FROM role_entitlements re WHERE re.role_id = $2::uuid`
	rows, err := tx.Query(ctx, `
		WITH `+fmt.Sprintf(held, "$1::uuid", extra)+`,
		     added AS (
		       SELECT $2::uuid AS role_id, $3::uuid AS entitlement_id
		       UNION SELECT NULL, re.entitlement_id FROM role_entitlements re WHERE re.role_id = $2::uuid
		     )
		SELECT p.name FROM sod_policies p
		WHERE p.mode = 'prevent' AND p.id IN (SELECT id FROM (`+conflicts+`) c(id, identity_id))
		  AND EXISTS (SELECT 1 FROM sod_policy_sides s JOIN added ad ON ad.role_id = s.role_id OR ad.entitlement_id = s.entitlement_id
		              WHERE s.policy_id = p.id)
		  AND NOT EXISTS (SELECT 1 FROM sod_violations v WHERE v.policy_id = p.id AND v.identity_id = $1 AND v.status = 'excepted')
		ORDER BY 1`, identityID, nullable(roleID), nullable(entitlementID))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ScanResult counts what a scan changed.
type ScanResult struct {
	Detected          int `json:"detected"`
	Resolved          int `json:"resolved"`
	ExceptionsExpired int `json:"exceptions_expired"`
	Open              int `json:"open"`
}

// Scan finds every violation, records new ones, resolves those that no
// longer apply and reopens exceptions past their end date.
func Scan(ctx context.Context, pool *pgxpool.Pool, tenantID string) (ScanResult, error) {
	var res ScanResult
	const actor = "system:sod"
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// One scan at a time.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('sod:' || $1))`, tenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`UPDATE sod_violations SET status = 'open', exception_by = NULL, exception_reason = NULL, exception_until = NULL
			 WHERE status = 'excepted' AND exception_until <= now() RETURNING id`)
		if err != nil {
			return err
		}
		expired, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range expired {
			if err := db.Audit(ctx, tx, tenantID, actor, "sod.exception_expired", "sod_violation", id, nil); err != nil {
				return err
			}
		}
		res.ExceptionsExpired = len(expired)

		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE sod_found ON COMMIT DROP AS WITH `+fmt.Sprintf(held, "NULL::uuid", "")+conflicts); err != nil {
			return err
		}
		rows, err = tx.Query(ctx,
			`INSERT INTO sod_violations (tenant_id, policy_id, identity_id)
			 SELECT $1, id, identity_id FROM sod_found
			 ON CONFLICT (policy_id, identity_id) WHERE status <> 'resolved' DO NOTHING
			 RETURNING id, policy_id, identity_id`, tenantID)
		if err != nil {
			return err
		}
		type found struct{ id, policy, identity string }
		scan := func(row pgx.CollectableRow) (found, error) {
			var f found
			err := row.Scan(&f.id, &f.policy, &f.identity)
			return f, err
		}
		added, err := pgx.CollectRows(rows, scan)
		if err != nil {
			return err
		}
		for _, f := range added {
			if err := db.Audit(ctx, tx, tenantID, actor, "sod.violation_detected", "identity", f.identity,
				map[string]any{"violation_id": f.id, "policy_id": f.policy}); err != nil {
				return err
			}
		}
		res.Detected = len(added)

		rows, err = tx.Query(ctx,
			`UPDATE sod_violations v SET status = 'resolved', resolved_at = now()
			 WHERE v.status <> 'resolved'
			   AND NOT EXISTS (SELECT 1 FROM sod_found f WHERE f.id = v.policy_id AND f.identity_id = v.identity_id)
			 RETURNING v.id, v.policy_id, v.identity_id`)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, scan)
		if err != nil {
			return err
		}
		for _, f := range gone {
			if err := db.Audit(ctx, tx, tenantID, actor, "sod.violation_resolved", "identity", f.identity,
				map[string]any{"violation_id": f.id, "policy_id": f.policy}); err != nil {
				return err
			}
		}
		res.Resolved = len(gone)
		return tx.QueryRow(ctx, `SELECT count(*) FROM sod_violations WHERE status = 'open'`).Scan(&res.Open)
	})
	return res, err
}

// Violation is someone holding both sides of a policy.
type Violation struct {
	ID              string     `json:"id"`
	PolicyID        string     `json:"policy_id"`
	PolicyName      string     `json:"policy_name"`
	Severity        string     `json:"severity"`
	IdentityID      string     `json:"identity_id"`
	IdentityName    string     `json:"identity_name"`
	Status          string     `json:"status"`
	DetectedAt      time.Time  `json:"detected_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	ExceptionBy     string     `json:"exception_by,omitempty"`
	ExceptionReason string     `json:"exception_reason,omitempty"`
	ExceptionUntil  *time.Time `json:"exception_until,omitempty"`
}

const selectViolations = `
	SELECT v.id, v.policy_id, p.name, p.severity, v.identity_id, i.display_name, v.status, v.detected_at, v.resolved_at,
	       coalesce(v.exception_by, ''), coalesce(v.exception_reason, ''), v.exception_until
	FROM sod_violations v JOIN sod_policies p ON p.id = v.policy_id JOIN identities i ON i.id = v.identity_id`

func scanViolation(row pgx.CollectableRow) (Violation, error) {
	var v Violation
	err := row.Scan(&v.ID, &v.PolicyID, &v.PolicyName, &v.Severity, &v.IdentityID, &v.IdentityName, &v.Status,
		&v.DetectedAt, &v.ResolvedAt, &v.ExceptionBy, &v.ExceptionReason, &v.ExceptionUntil)
	return v, err
}

// Filter selects violations.
type Filter struct {
	Status   string
	PolicyID string
	Limit    int
	After    string
}

// Violations lists violations, newest first by id.
func Violations(ctx context.Context, tx pgx.Tx, f Filter) ([]Violation, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Status != "" {
		if f.Status != Open && f.Status != Excepted && f.Status != Resolved {
			return nil, apperr.Invalidf("status must be open, excepted or resolved")
		}
		where = append(where, "v.status = "+arg(f.Status))
	}
	if f.PolicyID != "" {
		if !db.ValidUUID(f.PolicyID) {
			return nil, apperr.Invalidf("policy_id must be a policy id")
		}
		where = append(where, "v.policy_id = "+arg(f.PolicyID))
	}
	if f.After != "" {
		if !db.ValidUUID(f.After) {
			return nil, apperr.Invalidf("after must be a violation id")
		}
		where = append(where, "v.id > "+arg(f.After))
	}
	q := selectViolations
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY v.id LIMIT " + arg(f.Limit)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanViolation)
}

// Except accepts an open violation: the person keeps both sides, with a
// reason on record, until days have passed (0: no end date). The exception
// lasts while the violation does: once resolved, a new one starts open.
func Except(ctx context.Context, tx pgx.Tx, tenantID, actor, id, reason string, days int) (Violation, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return Violation{}, apperr.Invalidf("reason is required, up to 2000 characters: say why the risk is accepted and how it is mitigated")
	}
	if days < 0 || days > 365 {
		return Violation{}, apperr.Invalidf("days must be between 0 and 365")
	}
	if !db.ValidUUID(id) {
		return Violation{}, apperr.ErrNotFound
	}
	var until *time.Time
	if days > 0 {
		t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		until = &t
	}
	var status, identity string
	err := tx.QueryRow(ctx, `SELECT status, identity_id FROM sod_violations WHERE id = $1 FOR UPDATE`, id).Scan(&status, &identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return Violation{}, apperr.ErrNotFound
	}
	if err != nil {
		return Violation{}, err
	}
	if "identity:"+identity == actor {
		return Violation{}, apperr.Conflictf("nobody accepts their own violation")
	}
	if status != Open {
		return Violation{}, apperr.Conflictf("only open violations can be excepted; this one is %s", status)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sod_violations SET status = 'excepted', exception_by = $2, exception_reason = $3, exception_until = $4 WHERE id = $1`,
		id, actor, reason, until); err != nil {
		return Violation{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "sod.exception_granted", "sod_violation", id,
		map[string]any{"identity_id": identity, "reason": reason, "until": until}); err != nil {
		return Violation{}, err
	}
	rows, err := tx.Query(ctx, selectViolations+` WHERE v.id = $1`, id)
	if err != nil {
		return Violation{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanViolation)
}
