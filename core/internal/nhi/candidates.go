package nhi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
)

// Candidate is an active account nobody owns, which may be a service
// account, an agent or a workload waiting to be given an owner.
type Candidate struct {
	AccountID     string     `json:"account_id"`
	Username      string     `json:"username"`
	ApplicationID string     `json:"application_id"`
	Application   string     `json:"application"`
	Entitlements  int        `json:"entitlements"`
	Risky         bool       `json:"risky"`
	Vaulted       bool       `json:"vaulted"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
	// LooksLikeService says the name looks like a program's (svc-, -bot,
	// api, ...), not a person's.
	LooksLikeService bool `json:"looks_like_service"`
}

// serviceName is how service accounts, bots and agents tend to be named,
// as a Postgres regular expression matched ignoring case (~*).
const serviceName = `(^|[^a-z])(svc|srv|service|sa|bot|robot|agent|app|api|system|sys|daemon|batch|cron|job|ci|deploy|automation|integration|sync)([^a-z]|$)`

// MaxCandidates keeps the list to what anyone goes through at once.
const MaxCandidates = 500

// Candidates lists the active accounts no identity owns, outside Grantra's
// own application, the ones that look like a program's and hold high or
// critical risk access first, at most MaxCandidates.
func Candidates(ctx context.Context, tx pgx.Tx) ([]Candidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, u.name, app.id, app.name,
		       (SELECT count(*) FROM account_entitlements ae WHERE ae.account_id = a.id),
		       r.risky,
		       EXISTS (SELECT 1 FROM vault_accounts va WHERE va.account_id = a.id),
		       a.last_login_at, u.name ~* $2
		FROM accounts a JOIN applications app ON app.id = a.application_id
		CROSS JOIN LATERAL (SELECT coalesce(nullif(a.username, ''), a.native_id) AS name) u
		CROSS JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM account_entitlements ae JOIN entitlements e ON e.id = ae.entitlement_id
		                                   WHERE ae.account_id = a.id AND e.risk IN ('high', 'critical')) AS risky) r
		WHERE a.identity_id IS NULL AND a.status = 'active' AND app.connector <> $1
		ORDER BY u.name ~* $2 DESC, r.risky DESC, app.name, u.name, a.id
		LIMIT $3`, authz.BuiltinConnector, serviceName, MaxCandidates)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		var c Candidate
		err := row.Scan(&c.AccountID, &c.Username, &c.ApplicationID, &c.Application, &c.Entitlements, &c.Risky, &c.Vaulted,
			&c.LastLoginAt, &c.LooksLikeService)
		return c, err
	})
}
