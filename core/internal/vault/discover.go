package vault

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
)

// Reasons an account is a discovery finding.
const (
	// ReasonPrivileged: it holds a high or critical risk entitlement.
	ReasonPrivileged = "privileged"
	// ReasonOrphan: it belongs to nobody Grantra knows.
	ReasonOrphan = "orphan"
	// ReasonService: it belongs to a service account, an AI agent or a
	// workload, not a person.
	ReasonService = "service"
)

// Finding is an aggregated account that probably belongs in the vault.
type Finding struct {
	AccountID       string   `json:"account_id"`
	ApplicationID   string   `json:"application_id"`
	ApplicationName string   `json:"application_name"`
	Username        string   `json:"username"`
	NativeID        string   `json:"native_id"`
	IdentityID      string   `json:"identity_id,omitempty"`
	IdentityName    string   `json:"identity_name,omitempty"`
	Reasons         []string `json:"reasons"`
	// Entitlements are the high and critical risk entitlements it holds.
	Entitlements []string `json:"entitlements"`
	// CanRotate says whether its application can set its password.
	CanRotate bool `json:"can_rotate"`
	// VaultAccountID is set when the account is already in the vault.
	VaultAccountID string `json:"vault_account_id,omitempty"`
}

// Discover finds active accounts in the applications Grantra reads that are
// privileged, orphaned or used by a program (a service account, agent or workload), the ones in the vault included
// (with VaultAccountID set) so progress shows. At most limit come back,
// those outside the vault first.
func Discover(ctx context.Context, tx pgx.Tx, limit int) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		WITH privileged AS (
			SELECT ae.account_id, array_agg(e.name ORDER BY e.name) AS names
			FROM account_entitlements ae JOIN entitlements e ON e.id = ae.entitlement_id
			WHERE `+catalog.RiskySQL("e.risk")+` AND (ae.expires_at IS NULL OR ae.expires_at > now())
			GROUP BY ae.account_id
		)
		SELECT a.id, app.id, app.name, coalesce(a.username, ''), a.native_id,
		       coalesce(i.id::text, ''), coalesce(i.display_name, ''),
		       p.account_id IS NOT NULL, a.identity_id IS NULL, coalesce(i.kind <> 'human', false),
		       coalesce(p.names, '{}'), app.connector, coalesce(va.id::text, '')
		FROM accounts a
		JOIN applications app ON app.id = a.application_id
		LEFT JOIN identities i ON i.id = a.identity_id
		LEFT JOIN privileged p ON p.account_id = a.id
		LEFT JOIN vault_accounts va ON va.account_id = a.id
		WHERE a.status = 'active' AND app.connector NOT IN ('grantra', 'manual')
		  AND (p.account_id IS NOT NULL OR a.identity_id IS NULL OR i.kind <> 'human')
		ORDER BY va.id IS NOT NULL, p.account_id IS NULL, app.name, a.username, a.native_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Finding, error) {
		var (
			f                       Finding
			privileged, orphan, svc bool
			kind                    string
		)
		err := row.Scan(&f.AccountID, &f.ApplicationID, &f.ApplicationName, &f.Username, &f.NativeID,
			&f.IdentityID, &f.IdentityName, &privileged, &orphan, &svc, &f.Entitlements, &kind, &f.VaultAccountID)
		f.Reasons = []string{}
		if privileged {
			f.Reasons = append(f.Reasons, ReasonPrivileged)
		}
		if orphan {
			f.Reasons = append(f.Reasons, ReasonOrphan)
		}
		if svc {
			f.Reasons = append(f.Reasons, ReasonService)
		}
		f.CanRotate = connector.SetsPasswords(kind)
		return f, err
	})
}
