package reports

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	birthright "github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/login"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// guestAuditor is the value of access.GuestAttribute on external auditors.
const guestAuditor = "auditor"

// isExternalAuditor is true for an identity i that is an external auditor,
// written out so the partial index on them applies.
const isExternalAuditor = `i.attributes->>'` + birthright.GuestAttribute + `' = '` + guestAuditor + `'`

// ExternalAuditor is someone from outside the company with the auditor
// role until a date: they read everything and change nothing, every report
// they download is in the audit log, and on the day after Until they become
// a leaver, so their role ends and their accounts close.
type ExternalAuditor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company,omitempty"`
	// Until is their last day (UTC).
	Until string `json:"until"`
	// Status is the identity's: active, or terminated once their access ended.
	Status string `json:"status"`
	// Sponsor is the admin who invited them, who requests go to.
	SponsorID string    `json:"sponsor_id,omitempty"`
	Sponsor   string    `json:"sponsor,omitempty"`
	InvitedAt time.Time `json:"invited_at"`
	// Login is the state of their Grantra Login account: active, disabled,
	// or "" when they have none.
	Login string `json:"login,omitempty"`
}

// AuditorInput invites an external auditor, or moves their last day.
type AuditorInput struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	// Until is their last day, YYYY-MM-DD.
	Until string `json:"until"`
}

// ExternalAuditors lists the external auditors, those with access first.
func ExternalAuditors(ctx context.Context, tx pgx.Tx) ([]ExternalAuditor, error) {
	return listAuditors(ctx, tx, "")
}

// GetExternalAuditor returns the external auditor id.
func GetExternalAuditor(ctx context.Context, tx pgx.Tx, id string) (ExternalAuditor, error) {
	if !db.ValidUUID(id) {
		return ExternalAuditor{}, apperr.ErrNotFound
	}
	out, err := listAuditors(ctx, tx, id)
	if err == nil && len(out) == 0 {
		err = apperr.ErrNotFound
	}
	if err != nil {
		return ExternalAuditor{}, err
	}
	return out[0], nil
}

func listAuditors(ctx context.Context, tx pgx.Tx, id string) ([]ExternalAuditor, error) {
	rows, err := tx.Query(ctx,
		`SELECT i.id, i.display_name, coalesce(i.email, ''), coalesce(i.attributes->>'company', ''),
		        coalesce(to_char(i.end_date, 'YYYY-MM-DD'), ''), i.status, coalesce(i.manager_id::text, ''),
		        coalesce(m.display_name, ''), i.created_at,
		        coalesce((SELECT a.status FROM accounts a JOIN applications ap ON ap.id = a.application_id AND ap.connector = $1
		                  WHERE a.identity_id = i.id ORDER BY a.status = 'active' DESC LIMIT 1), '')
		 FROM identities i LEFT JOIN identities m ON m.id = i.manager_id
		 WHERE `+isExternalAuditor+` AND ($2 = '' OR i.id = nullif($2, '')::uuid)
		 ORDER BY i.status = 'terminated', i.end_date, lower(i.display_name), i.id`,
		grantralogin.Kind, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ExternalAuditor, error) {
		var x ExternalAuditor
		err := row.Scan(&x.ID, &x.Name, &x.Email, &x.Company, &x.Until, &x.Status, &x.SponsorID, &x.Sponsor, &x.InvitedAt, &x.Login)
		return x, err
	})
	if out == nil {
		out = []ExternalAuditor{}
	}
	return out, err
}

// checkUntil checks a last day: from today to as far ahead as the settings
// allow. It returns when the auditor role ends: the midnight after it.
func checkUntil(ctx context.Context, tx pgx.Tx, s string, now time.Time) (day, roleEnds time.Time, err error) {
	day, err = Day(s)
	if err != nil {
		return day, roleEnds, err
	}
	maxDays, err := settings.Get(ctx, tx, settings.ExternalAuditorDays)
	if err != nil {
		return day, roleEnds, err
	}
	today := now.UTC().Truncate(24 * time.Hour)
	switch {
	case day.Before(today):
		return day, roleEnds, apperr.Invalidf("the last day cannot be in the past")
	case day.After(today.AddDate(0, 0, maxDays)):
		return day, roleEnds, apperr.Invalidf("the last day can be at most %d days from today (Settings → General → Compliance reports)", maxDays)
	}
	return day, day.AddDate(0, 0, 1), nil
}

// InviteAuditor adds an external auditor, sponsored by the admin inviting
// them, with the auditor role until the end of their last day.
func InviteAuditor(ctx context.Context, tx pgx.Tx, tenantID string, by authz.Subject, in AuditorInput, now time.Time) (ExternalAuditor, error) {
	in.Name, in.Company = strings.TrimSpace(in.Name), strings.TrimSpace(in.Company)
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	switch {
	case in.Name == "" || len(in.Name) > 200:
		return ExternalAuditor{}, apperr.Invalidf("name must be between 1 and 200 characters")
	case err != nil || addr.Name != "" || len(addr.Address) > 320:
		return ExternalAuditor{}, apperr.Invalidf("email must be an email address, such as ines@auditfirm.com")
	case len(in.Company) > 200:
		return ExternalAuditor{}, apperr.Invalidf("company must be at most 200 characters")
	}
	until, roleEnds, err := checkUntil(ctx, tx, in.Until, now)
	if err != nil {
		return ExternalAuditor{}, err
	}
	attrs := map[string]string{birthright.GuestAttribute: guestAuditor}
	if in.Company != "" {
		attrs["company"] = in.Company
	}
	today := now.UTC().Truncate(24 * time.Hour)
	// Someone already in Grantra with this email: an external auditor whose
	// access ended is invited again; anyone else is not a guest.
	var (
		id, status string
		guest      bool
	)
	err = tx.QueryRow(ctx, `SELECT i.id, i.status, coalesce(`+isExternalAuditor+`, false) FROM identities i WHERE lower(i.email) = lower($1)
		ORDER BY i.status = 'terminated' LIMIT 1`, addr.Address).Scan(&id, &status, &guest)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx,
			`INSERT INTO identities (tenant_id, kind, status, display_name, email, manager_id, start_date, end_date, attributes)
			 VALUES ($1, 'human', 'active', $2, $3, $4, $5, $6, $7) RETURNING id`,
			tenantID, in.Name, addr.Address, db.Nullable(by.ID), today, until, attrs).Scan(&id)
	case err != nil:
	case guest && status == "terminated":
		_, err = tx.Exec(ctx,
			`UPDATE identities SET status = 'active', display_name = $2, manager_id = $3, start_date = $4, end_date = $5,
			        attributes = attributes - 'company' || $6, updated_at = now()
			 WHERE id = $1`, id, in.Name, db.Nullable(by.ID), today, until, attrs)
	case guest:
		return ExternalAuditor{}, apperr.Conflictf("%s is already invited: move their last day instead", addr.Address)
	default:
		return ExternalAuditor{}, apperr.Conflictf("someone in Grantra already has this email: give them the auditor role from their page instead")
	}
	if err != nil {
		return ExternalAuditor{}, err
	}
	if err := authz.Grant(ctx, tx, tenantID, authz.RoleAuditor, id, &roleEnds); err != nil {
		return ExternalAuditor{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, by.Actor(), "external_auditor.invited", "identity", id, map[string]any{
		"display_name": in.Name, "email": addr.Address, "company": in.Company, "until": in.Until,
	}); err != nil {
		return ExternalAuditor{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, by.Actor(), "platform_role.granted", "identity", id,
		map[string]any{"role": authz.RoleAuditor, "expires_at": roleEnds}); err != nil {
		return ExternalAuditor{}, err
	}
	return GetExternalAuditor(ctx, tx, id)
}

// ExtendAuditor moves the last day of an external auditor who still has
// access, earlier or later.
func ExtendAuditor(ctx context.Context, tx pgx.Tx, tenantID, actor, id, untilDay string, now time.Time) (ExternalAuditor, error) {
	cur, err := GetExternalAuditor(ctx, tx, id)
	if err != nil {
		return cur, err
	}
	if cur.Status == "terminated" {
		return cur, apperr.Invalidf("%s's access already ended: invite them again", cur.Name)
	}
	until, roleEnds, err := checkUntil(ctx, tx, untilDay, now)
	if err != nil {
		return cur, err
	}
	if _, err := tx.Exec(ctx, `UPDATE identities SET end_date = $2, updated_at = now() WHERE id = $1`, id, until); err != nil {
		return cur, err
	}
	if err := authz.Grant(ctx, tx, tenantID, authz.RoleAuditor, id, &roleEnds); err != nil {
		return cur, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "external_auditor.extended", "identity", id,
		map[string]any{"display_name": cur.Name, "from": cur.Until, "until": untilDay}); err != nil {
		return cur, err
	}
	return GetExternalAuditor(ctx, tx, id)
}

// EndAuditor ends an external auditor's access now: the role goes, and they
// become a leaver with today as their last day.
func EndAuditor(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, now time.Time) error {
	cur, err := GetExternalAuditor(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur.Status == "terminated" {
		return nil
	}
	if err := authz.Revoke(ctx, tx, authz.RoleAuditor, id); err != nil && !errors.Is(err, authz.ErrNotFound) {
		return err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "external_auditor.ended", "identity", id,
		map[string]any{"display_name": cur.Name, "until": cur.Until}); err != nil {
		return err
	}
	return lifecycle.Leave(ctx, tx, tenantID, actor, id, "external auditor's access ended early", now)
}

// CreateAuditorLogin gives an external auditor a Grantra Login account,
// which emails them a link to set their password. It returns
// login.ErrNotConnected when Grantra Login is not set up.
func CreateAuditorLogin(ctx context.Context, pool *pgxpool.Pool, tenantID, actor, id string, secrets connector.SecretResolver) error {
	var (
		app login.App
		c   *grantralogin.Client
		x   ExternalAuditor
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		if x, err = GetExternalAuditor(ctx, tx, id); err != nil {
			return err
		}
		app, c, err = login.Open(ctx, tx, secrets)
		return err
	})
	if err != nil {
		return err
	}
	native, err := c.CreateAccount(ctx, connector.NewAccount{Username: x.Email, Email: x.Email, DisplayName: x.Name})
	if err != nil {
		return err
	}
	// Someone invited again keeps the account their last visit disabled.
	if err := c.SetActive(ctx, native, true); err != nil {
		return err
	}
	return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (application_id, native_id) DO UPDATE SET identity_id = $3, status = 'active'`,
			tenantID, app.ID, id, native, x.Email); err != nil {
			return err
		}
		return db.Audit(ctx, tx, tenantID, actor, "external_auditor.login_created", "identity", id,
			map[string]any{"display_name": x.Name, "application_id": app.ID})
	})
}

// CloseAuditorLogins disables the Grantra Login accounts of external
// auditors whose access ended, so they cannot sign in to anything else the
// realm serves either. It does nothing when Grantra Login is not set up.
func CloseAuditorLogins(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) (int, error) {
	var (
		c     *grantralogin.Client
		open  []struct{ ID, Native, IdentityID string }
		appID string
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		app, cl, err := login.Open(ctx, tx, secrets)
		if err != nil {
			return err
		}
		c, appID = cl, app.ID
		rows, err := tx.Query(ctx,
			`SELECT a.id, a.native_id, i.id FROM accounts a JOIN identities i ON i.id = a.identity_id
			 WHERE a.application_id = $1 AND a.status = 'active' AND i.status = 'terminated' AND `+isExternalAuditor,
			app.ID)
		if err != nil {
			return err
		}
		open, err = pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Native, IdentityID string }])
		return err
	})
	if errors.Is(err, login.ErrNotConnected) || len(open) == 0 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	closed := 0
	var errs []error
	for _, a := range open {
		if err := c.SetActive(ctx, a.Native, false); err != nil && !grantralogin.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'disabled' WHERE id = $1`, a.ID); err != nil {
				return err
			}
			return db.Audit(ctx, tx, tenantID, ScheduleActor, "external_auditor.login_disabled", "identity", a.IdentityID,
				map[string]any{"account_id": a.ID, "application_id": appID})
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		closed++
	}
	return closed, errors.Join(errs...)
}
