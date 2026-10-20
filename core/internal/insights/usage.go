// Package insights works out what the access data says beyond who holds
// what: access nobody uses, what a reviewer should look at closely, and the
// business roles hiding in what people already hold. Everything here is
// counted from Grantra's own data and says why, so a person can check it;
// none of it changes access on its own.
package insights

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// limits are the tenant's thresholds for insights (package settings).
type limits struct {
	// unusedDays is how long access may go unused before Grantra calls it
	// unused, and how long someone must have held it first.
	unusedDays int
	// Role mining: the fewest people a group needs for its habits to mean
	// something, the share of a group (in percent) that must hold an
	// entitlement for it to belong in the group's role, and the fewest
	// entitlements worth a role.
	roleMinPeople, roleMinShare, roleMinEntitlements int
	// Peer groups smaller than these say nothing either way: the smallest
	// group whose habits make access look normal, and the smallest in
	// which holding something alone stands out.
	commonMinPeers, rareMinPeers int
}

func loadLimits(ctx context.Context, tx pgx.Tx) (limits, error) {
	v, err := settings.Values(ctx, tx, settings.UnusedDays, settings.RoleMinPeople, settings.RoleMinSharePercent,
		settings.RoleMinEntitlements, settings.CommonMinPeers, settings.RareMinPeers)
	return limits{
		unusedDays: v[settings.UnusedDays], roleMinPeople: v[settings.RoleMinPeople], roleMinShare: v[settings.RoleMinSharePercent],
		roleMinEntitlements: v[settings.RoleMinEntitlements], commonMinPeers: v[settings.CommonMinPeers], rareMinPeers: v[settings.RareMinPeers],
	}, err
}

// unusedSince is the moment before which use no longer counts.
func (l limits) unusedSince(now time.Time) time.Time { return now.AddDate(0, 0, -l.unusedDays) }

// Use is what Grantra knows about whether someone uses some access.
type Use struct {
	// LastUsedAt is the last time Grantra saw them use it.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// Tracked says whether Grantra sees this access being used at all: a
	// vault account, server or gateway target (Grantra is the way in), an
	// eligibility (Grantra turns it on), Grantra itself, or an application
	// whose accounts say when they last signed in or whose sign-ins Grantra
	// receives. Untracked access is never called unused.
	Tracked bool `json:"tracked"`
}

// entitlementUseSQL is a subquery giving, for the identity %[1]s and the
// entitlement ue of the application ua, when the identity last used it
// (at) and whether Grantra can tell (tracked). Its own aliases start with
// u, so they never hide the caller's.
const entitlementUseSQL = `
	SELECT greatest(
	         (SELECT max(uc.checked_out_at) FROM vault_checkouts uc JOIN vault_accounts uva ON uva.id = uc.vault_account_id
	          WHERE uva.entitlement_id = ue.id AND uc.identity_id = %[1]s),
	         (SELECT max(uc.issued_at) FROM ssh_certificates uc JOIN ssh_targets ut ON ut.id = uc.target_id
	          WHERE ut.entitlement_id = ue.id AND uc.identity_id = %[1]s),
	         (SELECT max(us.started_at) FROM gateway_sessions us JOIN gateway_targets ut ON ut.id = us.target_id
	          WHERE ut.entitlement_id = ue.id AND us.identity_id = %[1]s),
	         (SELECT max(uk.issued_at) FROM gateway_tokens uk JOIN gateway_targets ut ON ut.id = uk.target_id
	          WHERE ut.entitlement_id = ue.id AND uk.identity_id = %[1]s),
	         (SELECT max(ua2.last_login_at) FROM accounts ua2 WHERE ua2.application_id = ua.id AND ua2.identity_id = %[1]s),
	         (SELECT max(us.occurred_at) FROM signin_events us
	          WHERE us.identity_id = %[1]s AND us.outcome = 'success' AND us.application IS NOT NULL
	            AND lower(us.application) = lower(ua.name)),
	         CASE WHEN ua.connector = '%[2]s' THEN
	           (SELECT max(ul.last_login_at) FROM identity_logins ul WHERE ul.identity_id = %[1]s) END
	       ) AS at,
	       ua.connector = '%[2]s'
	         OR EXISTS (SELECT 1 FROM vault_accounts uva WHERE uva.entitlement_id = ue.id)
	         OR EXISTS (SELECT 1 FROM ssh_targets ut WHERE ut.entitlement_id = ue.id)
	         OR EXISTS (SELECT 1 FROM gateway_targets ut WHERE ut.entitlement_id = ue.id)
	         OR EXISTS (SELECT 1 FROM accounts ua2 WHERE ua2.application_id = ua.id AND ua2.last_login_at IS NOT NULL)
	         OR EXISTS (SELECT 1 FROM signin_events us WHERE us.outcome = 'success' AND us.application IS NOT NULL
	                    AND lower(us.application) = lower(ua.name)) AS tracked`

// useSQL is a LATERAL subquery giving, for the identity identity and the
// access role or entitlement (one of them NULL) held through the
// eligibility eligibility (NULL when held outright), all SQL expressions,
// when the identity last used it (at) and whether Grantra can tell
// (tracked). A role is used when any of its entitlements is, and tracked
// only when all of them are.
func useSQL(identity, role, entitlement, eligibility string) string {
	return fmt.Sprintf(`
	SELECT greatest(max(u.at), (SELECT uel.last_activated_at FROM access_eligibilities uel WHERE uel.id = %[4]s)) AS at,
	       coalesce(bool_and(u.tracked), false) OR %[4]s IS NOT NULL AS tracked
	FROM (SELECT %[3]s AS id WHERE %[3]s IS NOT NULL
	      UNION ALL SELECT entitlement_id FROM role_entitlements WHERE role_id = %[2]s) ke
	JOIN entitlements ue ON ue.id = ke.id
	JOIN applications ua ON ua.id = ue.application_id
	CROSS JOIN LATERAL (`+fmt.Sprintf(entitlementUseSQL, identity, authz.BuiltinConnector)+`) u`,
		identity, role, entitlement, eligibility)
}

// unused says whether access held since grantedAt, with use u, has gone
// unused: Grantra tracks it, it was held long enough, and nobody used it
// lately.
func (l limits) unused(u Use, grantedAt, now time.Time) bool {
	since := l.unusedSince(now)
	return u.Tracked && grantedAt.Before(since) && (u.LastUsedAt == nil || u.LastUsedAt.Before(since))
}
