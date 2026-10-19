// Package nhi governs non-human identities: service accounts, AI agents and
// workloads. Each one is an identity of its own kind with an owner, the
// person who answers for it. The owner is the identity's manager_id, so it
// reviews the identity's access and approves what it asks for exactly as a
// manager does for a person, and when the owner leaves, the identity passes
// to the owner's manager.
package nhi

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Kinds are the kinds of non-human identity.
var Kinds = []string{"service", "agent", "workload"}

// UnusedDays is how long a non-human identity may go without signing in
// anywhere before it counts as unused.
const UnusedDays = 90

// ExpiringDays is how soon an end date must be for the identity to count as
// expiring.
const ExpiringDays = 30

// Flags say what about a non-human identity needs a look.
const (
	// FlagNoOwner: nobody active answers for it.
	FlagNoOwner = "no_owner"
	// FlagUnused: its accounts say it has not signed in for UnusedDays.
	FlagUnused = "unused"
	// FlagUnvaulted: an account holding high or critical risk access keeps
	// its secret outside the vault, so nobody knows who has it or rotates it.
	FlagUnvaulted = "unvaulted"
	// FlagExpiring: it ends within ExpiringDays.
	FlagExpiring = "expiring"
)

// Flags lists every flag, in the order the console shows them.
var Flags = []string{FlagNoOwner, FlagUnvaulted, FlagUnused, FlagExpiring}

// Identity is a non-human identity and what Grantra knows about it.
type Identity struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose,omitempty"`
	SpiffeID string `json:"spiffe_id,omitempty"`
	Status   string `json:"status"`
	// OwnerID is the person who answers for it, and OwnerStatus whether they
	// are still active.
	OwnerID     string `json:"owner_id,omitempty"`
	OwnerName   string `json:"owner_name,omitempty"`
	OwnerStatus string `json:"owner_status,omitempty"`
	// EndDate is the last day it may exist (YYYY-MM-DD); after it, Grantra
	// retires it on its own.
	EndDate   string    `json:"end_date,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Accounts it has in applications, how many of them keep their secret in
	// the vault, and how many high and critical risk entitlements it holds.
	Accounts  int `json:"accounts"`
	Vaulted   int `json:"vaulted"`
	RiskyHeld int `json:"risky_held"`
	// LastUsedAt is the last sign-in its accounts or sign-ins show, when
	// any application says.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Flags      []string   `json:"flags"`
}

// statsSQL is a LATERAL subquery over the accounts of the identity i: how
// many it has, how many keep their secret in the vault, how many holding
// high or critical risk access do not, when it last signed in anywhere, and
// how many high and critical risk entitlements it holds, through Grantra or
// through its accounts.
const statsSQL = `
	SELECT count(*) AS accounts,
	       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM vault_accounts va WHERE va.account_id = a.id)) AS vaulted,
	       count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM vault_accounts va WHERE va.account_id = a.id)
	                          AND EXISTS (SELECT 1 FROM account_entitlements ae JOIN entitlements e ON e.id = ae.entitlement_id
	                                      WHERE ae.account_id = a.id AND e.risk IN ('high', 'critical')
	                                        AND (ae.expires_at IS NULL OR ae.expires_at > now()))) AS unvaulted,
	       greatest(max(a.last_login_at),
	                (SELECT max(se.occurred_at) FROM signin_events se WHERE se.identity_id = i.id AND se.outcome = 'success')) AS last_used_at,
	       (SELECT count(DISTINCT e.id) FROM entitlements e
	        WHERE e.risk IN ('high', 'critical')
	          AND (EXISTS (SELECT 1 FROM access_assignments x LEFT JOIN role_entitlements re ON re.role_id = x.role_id
	                       WHERE x.identity_id = i.id AND (x.entitlement_id = e.id OR re.entitlement_id = e.id)
	                         AND (x.expires_at IS NULL OR x.expires_at > now()))
	               OR EXISTS (SELECT 1 FROM account_entitlements ae JOIN accounts ha ON ha.id = ae.account_id
	                          WHERE ha.identity_id = i.id AND ha.status = 'active' AND ae.entitlement_id = e.id
	                            AND (ae.expires_at IS NULL OR ae.expires_at > now())))) AS risky
	FROM accounts a WHERE a.identity_id = i.id AND a.status <> 'deleted'`

// listSQL selects non-human identities with their stats and flags, as the
// columns scan reads. $1 is now.
var listSQL = `
	WITH n AS (
		SELECT i.id, i.kind, i.display_name, coalesce(i.purpose, '') AS purpose, coalesce(i.spiffe_id, '') AS spiffe_id,
		       i.status, coalesce(i.manager_id::text, '') AS owner_id, coalesce(o.display_name, '') AS owner_name,
		       coalesce(o.status, '') AS owner_status, i.end_date, i.created_at,
		       s.accounts, s.vaulted, s.risky, s.last_used_at,
		       i.status <> 'terminated' AND (o.id IS NULL OR o.status <> 'active') AS no_owner,
		       coalesce(i.status = 'active' AND s.last_used_at < $1::timestamptz - make_interval(days => ` + strconv.Itoa(UnusedDays) + `), false) AS unused,
		       i.status <> 'terminated' AND s.unvaulted > 0 AS unvaulted,
		       coalesce(i.status <> 'terminated' AND i.end_date < ($1::timestamptz + make_interval(days => ` + strconv.Itoa(ExpiringDays) + `))::date, false) AS expiring
		FROM identities i
		LEFT JOIN identities o ON o.id = i.manager_id
		CROSS JOIN LATERAL (` + statsSQL + `) s
		WHERE i.kind <> 'human'
	)`

const columns = `n.id, n.kind, n.display_name, n.purpose, n.spiffe_id, n.status, n.owner_id, n.owner_name, n.owner_status,
	coalesce(to_char(n.end_date, 'YYYY-MM-DD'), ''), n.created_at, n.accounts, n.vaulted, n.risky, n.last_used_at,
	n.no_owner, n.unused, n.unvaulted, n.expiring`

func scan(row pgx.CollectableRow) (Identity, error) {
	var (
		n                                    Identity
		noOwner, unused, unvaulted, expiring bool
	)
	err := row.Scan(&n.ID, &n.Kind, &n.Name, &n.Purpose, &n.SpiffeID, &n.Status, &n.OwnerID, &n.OwnerName, &n.OwnerStatus,
		&n.EndDate, &n.CreatedAt, &n.Accounts, &n.Vaulted, &n.RiskyHeld, &n.LastUsedAt,
		&noOwner, &unused, &unvaulted, &expiring)
	n.Flags = []string{}
	for _, f := range []struct {
		on   bool
		flag string
	}{{noOwner, FlagNoOwner}, {unvaulted, FlagUnvaulted}, {unused, FlagUnused}, {expiring, FlagExpiring}} {
		if f.on {
			n.Flags = append(n.Flags, f.flag)
		}
	}
	return n, err
}

// Filter selects non-human identities.
type Filter struct {
	// Kind is one of Kinds, or empty for all.
	Kind string
	// Flag is one of Flags, or empty for all.
	Flag string
	// Retired includes the ones retired; otherwise they are left out.
	Retired bool
	// Search keeps the ones whose name, purpose or SPIFFE ID contains it.
	Search string
	// After is the id of the last one of the previous page.
	After string
	Limit int
}

// Validate checks the filter's values.
func (f Filter) Validate() error {
	switch {
	case f.Kind != "" && !slices.Contains(Kinds, f.Kind):
		return apperr.Invalidf("kind must be service, agent or workload")
	case f.Flag != "" && !slices.Contains(Flags, f.Flag):
		return apperr.Invalidf("flag must be one of %s", strings.Join(Flags, ", "))
	case f.After != "" && !db.ValidUUID(f.After):
		return apperr.Invalidf("after must be an id from the list")
	case len(f.Search) > 200:
		return apperr.Invalidf("q must be at most 200 characters")
	}
	return nil
}

// List pages through non-human identities by id.
func List(ctx context.Context, tx pgx.Tx, f Filter) ([]Identity, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, listSQL+`
		SELECT `+columns+` FROM n
		WHERE ($2 = '' OR n.kind = $2) AND ($3 OR n.status <> 'terminated')
		  AND ($4 = '' OR lower(n.display_name) LIKE $5 OR lower(n.purpose) LIKE $5 OR lower(n.spiffe_id) LIKE $5)
		  AND ($6 = '' OR n.id > nullif($6, '')::uuid)
		  AND CASE $7 WHEN 'no_owner' THEN n.no_owner WHEN 'unused' THEN n.unused
		              WHEN 'unvaulted' THEN n.unvaulted WHEN 'expiring' THEN n.expiring ELSE true END
		ORDER BY n.id LIMIT $8`,
		time.Now(), f.Kind, f.Retired, f.Search, db.ContainsPattern(f.Search), f.After, f.Flag, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

// Get is one non-human identity.
func Get(ctx context.Context, tx pgx.Tx, id string) (Identity, error) {
	if !db.ValidUUID(id) {
		return Identity{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, listSQL+`SELECT `+columns+` FROM n WHERE n.id = $2`, time.Now(), id)
	if err != nil {
		return Identity{}, err
	}
	n, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, apperr.ErrNotFound
	}
	return n, err
}

// Summary counts the non-human identities that are not retired, in total
// and with each flag.
func Summary(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	var total, noOwner, unused, unvaulted, expiring int
	err := tx.QueryRow(ctx, listSQL+`
		SELECT count(*), count(*) FILTER (WHERE n.no_owner), count(*) FILTER (WHERE n.unused),
		       count(*) FILTER (WHERE n.unvaulted), count(*) FILTER (WHERE n.expiring)
		FROM n WHERE n.status <> 'terminated'`, time.Now()).Scan(&total, &noOwner, &unused, &unvaulted, &expiring)
	return map[string]int{"total": total, FlagNoOwner: noOwner, FlagUnused: unused, FlagUnvaulted: unvaulted,
		FlagExpiring: expiring}, err
}

// Input is a non-human identity to create, or the fields to change on one
// (nil leaves a field as it is; an empty string clears it).
type Input struct {
	Kind     *string `json:"kind"`
	Name     *string `json:"name"`
	Purpose  *string `json:"purpose"`
	OwnerID  *string `json:"owner_id"`
	SpiffeID *string `json:"spiffe_id"`
	EndDate  *string `json:"end_date"`
	// AccountIDs are accounts in applications to give it on creation: the
	// service account's logins, which usually show up as orphans.
	AccountIDs []string `json:"account_ids"`
}

// trustDomain is what the SPIFFE standard allows in a trust domain name.
var trustDomain = regexp.MustCompile(`^[a-z0-9._-]+$`)

// validSpiffeID checks a SPIFFE ID: spiffe://<trust domain>/<path>, with no
// empty, "." or ".." path segments, port, user, query or fragment.
func validSpiffeID(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "spiffe" || !trustDomain.MatchString(u.Host) || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || u.Port() != "" || len(s) > 2048 {
		return false
	}
	return u.Path == "" || (!strings.HasSuffix(u.Path, "/") && !slices.ContainsFunc(strings.Split(u.Path[1:], "/"),
		func(seg string) bool { return seg == "" || seg == "." || seg == ".." }))
}

// check validates in and the owner it names, and returns the end date.
func (in Input) check(ctx context.Context, tx pgx.Tx, id string) (*time.Time, error) {
	if in.Kind != nil && !slices.Contains(Kinds, *in.Kind) {
		return nil, apperr.Invalidf("kind must be service, agent or workload")
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 200) {
		return nil, apperr.Invalidf("name must be between 1 and 200 characters")
	}
	if in.Purpose != nil && len(*in.Purpose) > 500 {
		return nil, apperr.Invalidf("purpose must be at most 500 characters")
	}
	if in.SpiffeID != nil && *in.SpiffeID != "" && !validSpiffeID(*in.SpiffeID) {
		return nil, apperr.Invalidf("spiffe_id must look like spiffe://example.org/ns/prod/sa/billing")
	}
	if in.OwnerID != nil {
		var kind, status string
		if db.ValidUUID(*in.OwnerID) {
			err := tx.QueryRow(ctx, `SELECT kind, status FROM identities WHERE id = $1`, *in.OwnerID).Scan(&kind, &status)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
		if kind != "human" || status != "active" || *in.OwnerID == id {
			return nil, apperr.Invalidf("owner_id must be an active person: someone has to answer for it")
		}
	}
	var end *time.Time
	if in.EndDate != nil && *in.EndDate != "" {
		t, err := time.Parse(time.DateOnly, *in.EndDate)
		if err != nil {
			return nil, apperr.Invalidf("end_date must be a date such as 2027-03-31")
		}
		if t.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
			return nil, apperr.Invalidf("end_date must not be in the past")
		}
		end = &t
	}
	return end, nil
}

// spiffeTaken turns the unique index on SPIFFE IDs into a clear error.
func spiffeTaken(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "identities_spiffe_id" {
		return apperr.Conflictf("another identity already has that SPIFFE ID")
	}
	return err
}

// Create adds a non-human identity, active from now, and gives it the
// accounts listed.
func Create(ctx context.Context, tx pgx.Tx, tenantID, actor string, in Input) (Identity, error) {
	if in.Kind == nil || in.Name == nil || in.OwnerID == nil {
		return Identity{}, apperr.Invalidf("kind, name and owner_id are required")
	}
	end, err := in.check(ctx, tx, "")
	if err != nil {
		return Identity{}, err
	}
	if len(in.AccountIDs) > 100 {
		return Identity{}, apperr.Invalidf("give at most 100 accounts at once")
	}
	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO identities (tenant_id, kind, status, display_name, manager_id, purpose, spiffe_id, end_date)
		 VALUES ($1, $2, 'active', $3, $4, nullif($5, ''), nullif($6, ''), $7) RETURNING id`,
		tenantID, *in.Kind, strings.TrimSpace(*in.Name), *in.OwnerID, deref(in.Purpose), deref(in.SpiffeID), end).Scan(&id)
	if err != nil {
		return Identity{}, spiffeTaken(err)
	}
	for _, acc := range in.AccountIDs {
		if _, err := accounts.Link(ctx, tx, acc, id); err != nil {
			if errors.Is(err, apperr.ErrNotFound) {
				return Identity{}, apperr.Invalidf("account %s does not exist", acc)
			}
			return Identity{}, err
		}
	}
	err = db.Audit(ctx, tx, tenantID, actor, "identity.created", "identity", id, map[string]any{
		"kind": *in.Kind, "display_name": strings.TrimSpace(*in.Name), "owner_id": *in.OwnerID,
		"spiffe_id": deref(in.SpiffeID), "end_date": deref(in.EndDate), "account_ids": in.AccountIDs})
	if err != nil {
		return Identity{}, err
	}
	return Get(ctx, tx, id)
}

// Update changes the fields in set on the non-human identity id.
func Update(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in Input) (Identity, error) {
	cur, err := Get(ctx, tx, id)
	if err != nil {
		return Identity{}, err
	}
	if cur.Status == "terminated" {
		return Identity{}, apperr.Invalidf("%s is retired", cur.Name)
	}
	if in.Kind != nil || len(in.AccountIDs) > 0 {
		return Identity{}, apperr.Invalidf("kind and accounts cannot change: link accounts from the application's page")
	}
	end, err := in.check(ctx, tx, id)
	if err != nil {
		return Identity{}, err
	}
	_, err = tx.Exec(ctx,
		`UPDATE identities SET
		   display_name = coalesce($2, display_name),
		   purpose = CASE WHEN $3::text IS NULL THEN purpose ELSE nullif($3, '') END,
		   manager_id = coalesce($4::uuid, manager_id),
		   spiffe_id = CASE WHEN $5::text IS NULL THEN spiffe_id ELSE nullif($5, '') END,
		   end_date = CASE WHEN $6::text IS NULL THEN end_date ELSE $7::date END,
		   updated_at = now()
		 WHERE id = $1`,
		id, trimmed(in.Name), in.Purpose, in.OwnerID, in.SpiffeID, in.EndDate, end)
	if err != nil {
		return Identity{}, spiffeTaken(err)
	}
	changes := map[string]any{}
	for k, v := range map[string]*string{"display_name": in.Name, "purpose": in.Purpose, "owner_id": in.OwnerID,
		"spiffe_id": in.SpiffeID, "end_date": in.EndDate} {
		if v != nil {
			changes[k] = *v
		}
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "identity.updated", "identity", id, changes); err != nil {
		return Identity{}, err
	}
	return Get(ctx, tx, id)
}

// Retire ends the non-human identity id today, as a leaver: Grantra removes
// the access it was given, and provisioning disables its accounts.
func Retire(ctx context.Context, tx pgx.Tx, tenantID, actor, id, reason string) (Identity, error) {
	cur, err := Get(ctx, tx, id)
	if err != nil {
		return Identity{}, err
	}
	if cur.Status == "terminated" {
		return cur, nil
	}
	if len(reason) > 500 {
		return Identity{}, apperr.Invalidf("reason must be at most 500 characters")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE identities SET status = 'terminated', end_date = least(coalesce(end_date, current_date), current_date), updated_at = now()
		 WHERE id = $1`, id); err != nil {
		return Identity{}, err
	}
	details := map[string]any{"status": "terminated", "reason": reason}
	if _, err := tx.Exec(ctx,
		`INSERT INTO lifecycle_events (tenant_id, identity_id, kind, changes, effective_at) VALUES ($1, $2, 'leaver', $3, now())`,
		tenantID, id, details); err != nil {
		return Identity{}, err
	}
	details["display_name"] = cur.Name
	if err := db.Audit(ctx, tx, tenantID, actor, "identity.retired", "identity", id, details); err != nil {
		return Identity{}, err
	}
	return Get(ctx, tx, id)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}
