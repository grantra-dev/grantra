// Package login manages Grantra Login, the bundled Keycloak, from Grantra:
// the apps that sign in through it, its sign-in policy, and risk-based MFA,
// which keeps a realm role in step with who holds sensitive access.
//
// Grantra reaches Grantra Login through the application that uses the
// grantra-login connector, with that connector's service account.
package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// ErrNotConnected means no application uses the grantra-login connector.
var ErrNotConnected = errors.New("no application is connected to Grantra Login")

// App is the application Grantra Login is governed through.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Open returns the Grantra Login application and a client for its realm. With
// several such applications, the oldest one is used.
func Open(ctx context.Context, tx pgx.Tx, secrets connector.SecretResolver) (App, *grantralogin.Client, error) {
	var (
		app App
		cfg json.RawMessage
	)
	err := tx.QueryRow(ctx,
		`SELECT id, name, config FROM applications WHERE connector = $1 ORDER BY created_at, id LIMIT 1`,
		grantralogin.Kind).Scan(&app.ID, &app.Name, &cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return App{}, nil, ErrNotConnected
	}
	if err != nil {
		return App{}, nil, err
	}
	r, err := connector.New(grantralogin.Kind, cfg, secrets)
	if err != nil {
		return App{}, nil, fmt.Errorf("%s connector: %w", app.Name, err)
	}
	return app, r.(*grantralogin.Client), nil
}

// sensitiveRisks are the entitlement risks that call for a second factor.
var sensitiveRisks = []string{"high", "critical"}

// Sync is what one MFA sync changed.
type Sync struct {
	// Skipped is true when the sign-in policy is not MFASensitive, so the
	// role was left alone.
	Skipped bool `json:"skipped"`
	Added   int  `json:"added"`
	Removed int  `json:"removed"`
}

// SyncMFA keeps the MFARole realm role on exactly the Grantra Login accounts
// of active people who hold, or are assigned, a high or critical risk
// entitlement in any application. It only runs while the sign-in policy is
// MFASensitive. Every change is audited.
func SyncMFA(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) (Sync, error) {
	var (
		app   App
		c     *grantralogin.Client
		want  = map[string]string{} // account native id -> identity id
		owner = map[string]string{} // native id -> identity id, for every linked account
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		if app, c, err = Open(ctx, tx, secrets); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			WITH sensitive AS (
				SELECT a.identity_id FROM account_entitlements ae
				JOIN accounts a ON a.id = ae.account_id
				JOIN entitlements e ON e.id = ae.entitlement_id
				WHERE e.risk = ANY($2) AND a.status = 'active' AND a.identity_id IS NOT NULL
				UNION
				SELECT x.identity_id FROM access_assignments x
				LEFT JOIN role_entitlements re ON re.role_id = x.role_id
				JOIN entitlements e ON e.id = coalesce(x.entitlement_id, re.entitlement_id)
				WHERE e.risk = ANY($2) AND (x.expires_at IS NULL OR x.expires_at > now())
			)
			SELECT a.native_id, a.identity_id, (s.identity_id IS NOT NULL AND i.status = 'active')
			FROM accounts a
			JOIN identities i ON i.id = a.identity_id
			LEFT JOIN sensitive s ON s.identity_id = a.identity_id
			WHERE a.application_id = $1 AND a.status <> 'deleted'`, app.ID, sensitiveRisks)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var native, identity string
			var sensitive bool
			if err := rows.Scan(&native, &identity, &sensitive); err != nil {
				return err
			}
			owner[native] = identity
			if sensitive {
				want[native] = identity
			}
		}
		return rows.Err()
	})
	if errors.Is(err, ErrNotConnected) {
		return Sync{Skipped: true}, nil
	}
	if err != nil {
		return Sync{}, err
	}

	// The calls to Keycloak happen outside any transaction.
	p, err := c.Policy(ctx)
	if err != nil {
		return Sync{}, err
	}
	if p.MFA != grantralogin.MFASensitive {
		return Sync{Skipped: true}, nil
	}
	holders, err := c.RoleHolders(ctx, grantralogin.MFARole)
	if err != nil {
		return Sync{}, err
	}
	has := map[string]bool{}
	for _, id := range holders {
		has[id] = true
	}
	var out Sync
	record := func(native string, added bool) error {
		action := "login.mfa_required"
		if !added {
			action = "login.mfa_not_required"
		}
		return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			target := owner[native]
			kind := "identity"
			if target == "" {
				target, kind = native, "account"
			}
			return db.Audit(ctx, tx, tenantID, "system:login", action, kind, target,
				map[string]any{"application_id": app.ID, "account": native, "role": grantralogin.MFARole})
		})
	}
	role := "role:" + grantralogin.MFARole
	for native := range want {
		if has[native] {
			continue
		}
		if err := c.AddMember(ctx, role, native); err != nil {
			return out, err
		}
		out.Added++
		if err := record(native, true); err != nil {
			return out, err
		}
	}
	// The role is Grantra's: anyone else in it is taken out.
	for native := range has {
		if _, ok := want[native]; ok {
			continue
		}
		if err := c.RemoveMember(ctx, role, native); err != nil {
			return out, err
		}
		out.Removed++
		if err := record(native, false); err != nil {
			return out, err
		}
	}
	return out, nil
}
