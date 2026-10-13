package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Account is a login found in an application.
type Account struct {
	ID              string            `json:"id"`
	ApplicationID   string            `json:"application_id"`
	ApplicationName string            `json:"application_name"`
	NativeID        string            `json:"native_id"`
	Username        string            `json:"username,omitempty"`
	Status          string            `json:"status"`
	IdentityID      string            `json:"identity_id,omitempty"`
	IdentityName    string            `json:"identity_name,omitempty"`
	CorrelatedBy    string            `json:"correlated_by,omitempty"`
	Attributes      map[string]string `json:"attributes"`
	Entitlements    []string          `json:"entitlements"`
	LastSeenAt      *time.Time        `json:"last_seen_at,omitempty"`
}

const accountQuery = `SELECT a.id, a.application_id, app.name, a.native_id, coalesce(a.username, ''), a.status,
	coalesce(a.identity_id::text, ''), coalesce(i.display_name, ''), coalesce(a.correlated_by, ''), a.attributes,
	ARRAY(SELECT e.name FROM account_entitlements ae JOIN entitlements e ON e.id = ae.entitlement_id
	      WHERE ae.account_id = a.id ORDER BY e.name),
	a.last_seen_at
	FROM accounts a
	JOIN applications app ON app.id = a.application_id
	LEFT JOIN identities i ON i.id = a.identity_id `

func scanAccount(row pgx.CollectableRow) (Account, error) {
	var (
		a     Account
		attrs map[string]any
	)
	err := row.Scan(&a.ID, &a.ApplicationID, &a.ApplicationName, &a.NativeID, &a.Username, &a.Status,
		&a.IdentityID, &a.IdentityName, &a.CorrelatedBy, &attrs, &a.Entitlements, &a.LastSeenAt)
	a.Attributes = map[string]string{}
	for k, v := range attrs {
		if s, ok := v.(string); ok {
			a.Attributes[k] = s
		}
	}
	return a, err
}

// Filter narrows a listing of an application's accounts.
type Filter struct {
	// Orphans keeps accounts linked to nobody that still exist.
	Orphans bool
	// After pages by account id.
	After string
	Limit int
}

// List returns an application's accounts, by id.
func List(ctx context.Context, tx pgx.Tx, applicationID string, f Filter) ([]Account, error) {
	if !db.ValidUUID(applicationID) {
		return nil, apperr.ErrNotFound
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE id = $1)`, applicationID).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, accountQuery+
		`WHERE a.application_id = $1 AND ($2 = '' OR a.id > $2::uuid)
		   AND (NOT $3 OR (a.identity_id IS NULL AND a.status <> 'deleted'))
		 ORDER BY a.id LIMIT $4`, applicationID, f.After, f.Orphans, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanAccount)
}

// ForIdentity returns every account linked to an identity.
func ForIdentity(ctx context.Context, tx pgx.Tx, identityID string) ([]Account, error) {
	rows, err := tx.Query(ctx, accountQuery+`WHERE a.identity_id = $1 ORDER BY app.name, a.native_id`, identityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanAccount)
}

// Link ties an account to an identity by hand, or with identityID empty marks
// it as nobody's (a shared or service login). Either way, aggregations stop
// correlating it by rule.
func Link(ctx context.Context, tx pgx.Tx, accountID, identityID string) (Account, error) {
	if !db.ValidUUID(accountID) {
		return Account{}, apperr.ErrNotFound
	}
	var ident *string
	if identityID != "" {
		var ok bool
		if db.ValidUUID(identityID) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identities WHERE id = $1)`, identityID).Scan(&ok); err != nil {
				return Account{}, err
			}
		}
		if !ok {
			return Account{}, apperr.Invalidf("identity %s does not exist", identityID)
		}
		ident = &identityID
	}
	var connector string
	err := tx.QueryRow(ctx,
		`SELECT app.connector FROM accounts a JOIN applications app ON app.id = a.application_id WHERE a.id = $1 FOR UPDATE OF a`,
		accountID).Scan(&connector)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, apperr.ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	if connector == builtinConnector {
		return Account{}, apperr.Invalidf("accounts of the built-in Grantra application follow platform roles")
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET identity_id = $2, correlated_by = $3 WHERE id = $1`, accountID, ident, ByHand); err != nil {
		return Account{}, err
	}
	rows, err := tx.Query(ctx, accountQuery+`WHERE a.id = $1`, accountID)
	if err != nil {
		return Account{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanAccount)
}

// Runs returns the latest connector runs of an application, newest first.
func Runs(ctx context.Context, tx pgx.Tx, applicationID string, limit int) ([]Run, error) {
	if !db.ValidUUID(applicationID) {
		return nil, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx,
		`SELECT id, application_id, kind, actor, status, counts, coalesce(error, ''), started_at, finished_at
		 FROM connector_runs WHERE application_id = $1 ORDER BY started_at DESC LIMIT $2`, applicationID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		var r Run
		err := row.Scan(&r.ID, &r.ApplicationID, &r.Kind, &r.Actor, &r.Status, &r.Counts, &r.Error, &r.StartedAt, &r.FinishedAt)
		return r, err
	})
}
