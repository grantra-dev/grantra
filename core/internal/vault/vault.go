// Package vault keeps the credentials of privileged accounts: root on a
// database, a domain admin, a break-glass user. Secrets are sealed with
// envelope encryption (see KeyProvider), checked out to the people who hold
// access to them for a limited time, and rotated in the target system (see
// rotate.go).
//
// Who may check out a vault account is an entitlement in the "Privileged
// vault" application, created with the account. Requests, approvals,
// reviews, separation of duties and expiry therefore govern the vault like
// any other access, and admins cannot read a secret unless they hold it too.
package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

// ApplicationName is the application whose entitlements give access to
// vault accounts.
const ApplicationName = "Privileged vault"

// Secret kinds.
const (
	KindPassword = "password"
	KindSSHKey   = "ssh_key"
)

// maxSecret bounds a secret's size: an RSA 4096 private key is about 3.3 KB.
const maxSecret = 16 << 10

// ErrDisabled is returned when the vault has no key to seal secrets with.
var ErrDisabled = apperr.Conflictf("the vault is off: set %s to 32 random bytes in base64 (openssl rand -base64 32) and restart Grantra", KeyEnv)

// Vault seals and opens secrets with its key provider. Keys may be nil: the
// vault then lists and manages accounts but cannot store or reveal secrets.
type Vault struct {
	keys    KeyProvider
	secrets connector.SecretResolver
	now     func() time.Time
}

// New returns a vault using keys to seal secrets and secrets to resolve the
// credentials of the connectors that rotate passwords.
func New(keys KeyProvider, secrets connector.SecretResolver) *Vault {
	return &Vault{keys: keys, secrets: secrets, now: time.Now}
}

// Enabled reports whether the vault can store and reveal secrets.
func (v *Vault) Enabled() bool { return v != nil && v.keys != nil }

// Account is a vault account, never with its secret.
type Account struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Username        string `json:"username"`
	Address         string `json:"address,omitempty"`
	Notes           string `json:"notes,omitempty"`
	Kind            string `json:"kind"`
	AccountID       string `json:"account_id,omitempty"`
	ApplicationID   string `json:"application_id,omitempty"`
	ApplicationName string `json:"application_name,omitempty"`
	EntitlementID   string `json:"entitlement_id"`
	EntitlementName string `json:"entitlement_name"`
	Risk            string `json:"risk"`
	CheckoutMinutes int    `json:"checkout_minutes"`
	Exclusive       bool   `json:"exclusive"`
	// RotateEveryDays and RotateAfterCheckin are the rotation policy;
	// CanRotate says whether the account's application can change passwords.
	RotateEveryDays    int        `json:"rotate_every_days,omitempty"`
	RotateAfterCheckin bool       `json:"rotate_after_checkin"`
	CanRotate          bool       `json:"can_rotate"`
	RotateAt           *time.Time `json:"rotate_at,omitempty"`
	RotationFailures   int        `json:"rotation_failures"`
	LastRotationError  string     `json:"last_rotation_error,omitempty"`
	// Version is the current secret's version, 0 when none is stored yet.
	Version   int        `json:"version"`
	SecretAt  *time.Time `json:"secret_at,omitempty"`
	Checkouts []Checkout `json:"checkouts"`
	// BreakGlass accounts open in an emergency without approval, for the
	// responders (the platform admins when none are listed).
	BreakGlass bool        `json:"break_glass"`
	Responders []Responder `json:"break_glass_responders"`
	// Held says whether the person asking holds access to it, and so can
	// check it out; Responder whether they may break the glass. Set by the
	// console.
	Held      bool      `json:"held"`
	Responder bool      `json:"responder"`
	CreatedAt time.Time `json:"created_at"`
}

// Responder is someone who may open a break-glass account.
type Responder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Checkout is one lease of a vault account's secret.
type Checkout struct {
	ID             string     `json:"id"`
	VaultAccountID string     `json:"vault_account_id"`
	IdentityID     string     `json:"identity_id"`
	IdentityName   string     `json:"identity_name"`
	Version        int        `json:"version"`
	Reason         string     `json:"reason"`
	CheckedOutAt   time.Time  `json:"checked_out_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	EndedBy        string     `json:"ended_by,omitempty"`
	// BreakGlass check-outs are reviewed afterwards: ReviewStatus is
	// pending, justified or flagged.
	BreakGlass    bool       `json:"break_glass"`
	ReviewStatus  string     `json:"review_status,omitempty"`
	ReviewedBy    string     `json:"reviewed_by,omitempty"`
	ReviewedAt    *time.Time `json:"reviewed_at,omitempty"`
	ReviewComment string     `json:"review_comment,omitempty"`
	AccountName   string     `json:"account_name"`
}

// Version is one value a vault account has had, without the value.
type Version struct {
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	KeyID     string    `json:"key_id"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Settings are the parts of a vault account that can change after it is
// created.
type Settings struct {
	Address            string `json:"address"`
	Notes              string `json:"notes"`
	CheckoutMinutes    int    `json:"checkout_minutes"`
	Exclusive          bool   `json:"exclusive"`
	RotateEveryDays    int    `json:"rotate_every_days"`
	RotateAfterCheckin bool   `json:"rotate_after_checkin"`
	// BreakGlass lets Responders (identity ids; the platform admins when
	// empty) open the account in an emergency without approval.
	BreakGlass bool     `json:"break_glass"`
	Responders []string `json:"break_glass_responders"`
}

// Input describes a new vault account. AccountID links it to an aggregated
// account, whose application then rotates its password and whose username
// it takes when Username is empty. Secret is optional: it can be set later,
// or a rotation can set the first one.
type Input struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Kind     string `json:"kind"`
	// Risk is the risk of the entitlement that gives access; critical by
	// default.
	Risk      string `json:"risk"`
	AccountID string `json:"account_id"`
	Secret    string `json:"secret"`
	Settings
}

func (s *Settings) check(kind string, canRotate bool) error {
	s.Address = strings.TrimSpace(s.Address)
	s.Notes = strings.TrimSpace(s.Notes)
	switch {
	case len(s.Address) > 500:
		return apperr.Invalidf("address must be at most 500 characters")
	case len(s.Notes) > 2000:
		return apperr.Invalidf("notes must be at most 2000 characters")
	case s.CheckoutMinutes == 0:
		s.CheckoutMinutes = 60
	}
	if s.CheckoutMinutes < 5 || s.CheckoutMinutes > 480 {
		return apperr.Invalidf("checkout_minutes must be between 5 and 480")
	}
	s.Responders = slices.Compact(slices.Sorted(slices.Values(s.Responders)))
	if len(s.Responders) > 50 {
		return apperr.Invalidf("list at most 50 break-glass responders")
	}
	for _, r := range s.Responders {
		if !db.ValidUUID(r) {
			return apperr.Invalidf("break_glass_responders must be identity ids")
		}
	}
	if len(s.Responders) > 0 && !s.BreakGlass {
		return apperr.Invalidf("break-glass responders are only for break-glass accounts")
	}
	if s.RotateEveryDays < 0 || s.RotateEveryDays > 365 {
		return apperr.Invalidf("rotate_every_days must be between 1 and 365, or 0 for no schedule")
	}
	if s.RotateEveryDays > 0 || s.RotateAfterCheckin {
		if kind != KindPassword {
			return apperr.Invalidf("only passwords rotate")
		}
		if !canRotate {
			return apperr.Invalidf("rotation needs the vault account linked to an account in an application whose connector can set passwords (%s)", strings.Join(rotatingKinds(), ", "))
		}
	}
	return nil
}

func rotatingKinds() []string {
	var out []string
	for _, k := range connector.Kinds() {
		if connector.SetsPasswords(k) {
			out = append(out, k)
		}
	}
	return out
}

// Create adds a vault account and the entitlement that gives access to it.
func (v *Vault) Create(ctx context.Context, tx pgx.Tx, tenantID, actor string, in Input) (Account, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 200 {
		return Account{}, apperr.Invalidf("name must be between 1 and 200 characters")
	}
	if in.Kind == "" {
		in.Kind = KindPassword
	}
	if in.Kind != KindPassword && in.Kind != KindSSHKey {
		return Account{}, apperr.Invalidf("kind must be %s or %s", KindPassword, KindSSHKey)
	}
	if in.Risk == "" {
		in.Risk = "critical"
	}
	if in.Risk != "high" && in.Risk != "critical" {
		return Account{}, apperr.Invalidf("risk must be high or critical: vault accounts are privileged")
	}
	canRotate := false
	username := strings.TrimSpace(in.Username)
	if in.AccountID != "" {
		if !db.ValidUUID(in.AccountID) {
			return Account{}, apperr.Invalidf("account_id must be an account id")
		}
		var (
			native, kind, appUsername string
		)
		err := tx.QueryRow(ctx,
			`SELECT a.native_id, coalesce(a.username, ''), app.connector FROM accounts a JOIN applications app ON app.id = a.application_id
			 WHERE a.id = $1`, in.AccountID).Scan(&native, &appUsername, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, apperr.Invalidf("account_id names no account")
		}
		if err != nil {
			return Account{}, err
		}
		if username == "" {
			username = cmpOr(appUsername, native)
		}
		canRotate = connector.SetsPasswords(kind)
	}
	if username == "" || len(username) > 300 {
		return Account{}, apperr.Invalidf("username must be between 1 and 300 characters")
	}
	if err := in.Settings.check(in.Kind, canRotate); err != nil {
		return Account{}, err
	}
	if len(in.Secret) > maxSecret {
		return Account{}, apperr.Invalidf("secret must be at most %d bytes", maxSecret)
	}
	if in.Secret != "" && !v.Enabled() {
		return Account{}, ErrDisabled
	}

	appID, err := ensureApplication(ctx, tx, tenantID)
	if err != nil {
		return Account{}, err
	}
	desc := "Check out " + username
	if in.Settings.Address != "" {
		desc += " on " + in.Settings.Address
	}
	desc += " from the vault."
	ent, err := catalog.CreateEntitlement(ctx, tx, tenantID, appID, catalog.EntitlementInput{
		NativeID: "vault:" + name, Name: "Use " + name, Description: desc, Risk: in.Risk})
	if err != nil {
		var c *apperr.Conflict
		if errors.As(err, &c) {
			return Account{}, apperr.Conflictf("a vault account named %q already exists", name)
		}
		return Account{}, err
	}
	// Nobody holds vault access for good: people request it for a time or
	// are made eligible and activate it.
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET jit_only = true WHERE id = $1`, ent.ID); err != nil {
		return Account{}, err
	}
	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO vault_accounts (tenant_id, name, username, address, notes, kind, account_id, entitlement_id,
		                             checkout_minutes, exclusive, rotate_every_days, rotate_after_checkin, break_glass, created_by,
		                             break_glass_set_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, 0), $12, $13, $14, CASE WHEN $13 THEN $14 END)
		 ON CONFLICT DO NOTHING RETURNING id`,
		tenantID, name, username, db.Nullable(in.Settings.Address), db.Nullable(in.Settings.Notes), in.Kind, db.Nullable(in.AccountID), ent.ID,
		in.Settings.CheckoutMinutes, in.Settings.Exclusive, in.Settings.RotateEveryDays, in.Settings.RotateAfterCheckin,
		in.Settings.BreakGlass, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, apperr.Conflictf("a vault account named %q, or one for that account, already exists", name)
	}
	if err != nil {
		return Account{}, err
	}
	if err := setResponders(ctx, tx, tenantID, id, in.Settings.Responders); err != nil {
		return Account{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "vault.account_created", "vault_account", id,
		map[string]any{"name": name, "username": username, "entitlement_id": ent.ID,
			"break_glass": in.Settings.BreakGlass, "break_glass_responders": in.Settings.Responders}); err != nil {
		return Account{}, err
	}
	if in.Secret != "" {
		if err := v.setSecret(ctx, tx, tenantID, actor, id, in.Secret); err != nil {
			return Account{}, err
		}
	}
	return Get(ctx, tx, id)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ensureApplication returns the "Privileged vault" application, creating it
// the first time. It is an application governed by hand: nothing to read or
// provision, only access to grant.
func ensureApplication(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var id, kind string
	err := tx.QueryRow(ctx, `SELECT id, connector FROM applications WHERE name = $1`, ApplicationName).Scan(&id, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		app, err := catalog.CreateApplication(ctx, tx, tenantID, ApplicationName, "")
		return app.ID, err
	}
	if err != nil {
		return "", err
	}
	if kind != catalog.ManualConnector {
		return "", apperr.Conflictf("the application %q has a connector; the vault needs that name for its own application, so rename the other one", ApplicationName)
	}
	return id, nil
}

// lock loads a vault account for update.
func lock(ctx context.Context, tx pgx.Tx, id string) (kind string, linked bool, canRotate bool, err error) {
	if !db.ValidUUID(id) {
		return "", false, false, apperr.ErrNotFound
	}
	var connKind string
	err = tx.QueryRow(ctx,
		`SELECT va.kind, va.account_id IS NOT NULL, coalesce(app.connector, '')
		 FROM vault_accounts va
		 LEFT JOIN accounts a ON a.id = va.account_id
		 LEFT JOIN applications app ON app.id = a.application_id
		 WHERE va.id = $1 FOR UPDATE OF va`, id).Scan(&kind, &linked, &connKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, apperr.ErrNotFound
	}
	return kind, linked, linked && connector.SetsPasswords(connKind), err
}

// Update changes a vault account's settings.
func (v *Vault) Update(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, s Settings) (Account, error) {
	kind, _, canRotate, err := lock(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if err := s.check(kind, canRotate); err != nil {
		return Account{}, err
	}
	// Turning break-glass on or changing who responds re-arms it: whoever
	// does that may not open it themselves.
	var (
		wasGlass bool
		before   []string
	)
	if err := tx.QueryRow(ctx,
		`SELECT va.break_glass, coalesce(array_agg(r.identity_id::text ORDER BY r.identity_id::text COLLATE "C") FILTER (WHERE r.identity_id IS NOT NULL), '{}')
		 FROM vault_accounts va LEFT JOIN vault_break_glass_responders r ON r.vault_account_id = va.id
		 WHERE va.id = $1 GROUP BY va.break_glass`, id).Scan(&wasGlass, &before); err != nil {
		return Account{}, err
	}
	rearmed := s.BreakGlass && (!wasGlass || !slices.Equal(before, s.Responders))
	// A new schedule counts from the current secret; turning it off clears
	// the next date unless a rotation is already owed for another reason.
	_, err = tx.Exec(ctx,
		`UPDATE vault_accounts SET address = $2, notes = $3, checkout_minutes = $4, exclusive = $5,
		        rotate_every_days = nullif($6, 0), rotate_after_checkin = $7, break_glass = $8, updated_at = now(),
		        break_glass_set_by = CASE WHEN NOT $8 THEN NULL WHEN $9 THEN $10 ELSE break_glass_set_by END,
		        rotate_at = CASE
		            WHEN rotation_failures > 0 OR rotate_at <= now() THEN rotate_at
		            WHEN $6 = 0 THEN NULL
		            ELSE (SELECT created_at FROM vault_secret_versions s WHERE s.vault_account_id = $1 AND s.status = 'current') + make_interval(days => $6)
		        END
		 WHERE id = $1`,
		id, db.Nullable(s.Address), db.Nullable(s.Notes), s.CheckoutMinutes, s.Exclusive, s.RotateEveryDays, s.RotateAfterCheckin, s.BreakGlass,
		rearmed, actor)
	if err != nil {
		return Account{}, err
	}
	if err := setResponders(ctx, tx, tenantID, id, s.Responders); err != nil {
		return Account{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "vault.account_updated", "vault_account", id, map[string]any{
		"checkout_minutes": s.CheckoutMinutes, "exclusive": s.Exclusive,
		"rotate_every_days": s.RotateEveryDays, "rotate_after_checkin": s.RotateAfterCheckin,
		"break_glass": s.BreakGlass, "break_glass_responders": s.Responders}); err != nil {
		return Account{}, err
	}
	return Get(ctx, tx, id)
}

// Delete removes a vault account, its secret history and the entitlement
// that gave access to it, with every assignment and request of it. Open
// check-outs end with it.
func Delete(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if _, _, _, err := lock(ctx, tx, id); err != nil {
		return err
	}
	var name, entitlement string
	err := tx.QueryRow(ctx, `DELETE FROM vault_accounts WHERE id = $1 RETURNING name, entitlement_id`, id).Scan(&name, &entitlement)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE id = $1`, entitlement); err != nil {
		return fmt.Errorf("delete the vault account's entitlement: %w", err)
	}
	return db.Audit(ctx, tx, tenantID, actor, "vault.account_deleted", "vault_account", id, map[string]any{"name": name})
}

// SetSecret stores a new value for a vault account, typed by an admin. It
// becomes the current version at once; a rotation in flight is abandoned.
func (v *Vault) SetSecret(ctx context.Context, tx pgx.Tx, tenantID, actor, id, secret string) error {
	if !v.Enabled() {
		return ErrDisabled
	}
	if secret == "" || len(secret) > maxSecret {
		return apperr.Invalidf("secret must be between 1 and %d bytes", maxSecret)
	}
	if _, _, _, err := lock(ctx, tx, id); err != nil {
		return err
	}
	return v.setSecret(ctx, tx, tenantID, actor, id, secret)
}

// setSecret stores secret as the current version of a locked account.
func (v *Vault) setSecret(ctx context.Context, tx pgx.Tx, tenantID, actor, id, secret string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE vault_secret_versions SET status = 'failed' WHERE vault_account_id = $1 AND status = 'pending'`, id); err != nil {
		return err
	}
	version, err := v.insertVersion(ctx, tx, tenantID, actor, id, secret, "set", "current")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE vault_accounts SET rotation_failures = 0, last_rotation_error = NULL, updated_at = now(),
		        rotate_at = now() + make_interval(days => rotate_every_days)
		 WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "vault.secret_set", "vault_account", id, map[string]any{"version": version})
}

// insertVersion seals secret as the next version of a locked account. A new
// current version retires the previous one.
func (v *Vault) insertVersion(ctx context.Context, tx pgx.Tx, tenantID, actor, id, secret, source, status string) (int, error) {
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) + 1 FROM vault_secret_versions WHERE vault_account_id = $1`, id).Scan(&version); err != nil {
		return 0, err
	}
	s, err := seal(v.keys, []byte(secret), versionAAD(tenantID, id, version))
	if err != nil {
		return 0, err
	}
	if status == "current" {
		if _, err := tx.Exec(ctx,
			`UPDATE vault_secret_versions SET status = 'retired' WHERE vault_account_id = $1 AND status = 'current'`, id); err != nil {
			return 0, err
		}
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO vault_secret_versions (tenant_id, vault_account_id, version, status, source, key_id, wrapped_key, nonce, ciphertext, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		tenantID, id, version, status, source, s.KeyID, s.WrappedKey, s.Nonce, s.Ciphertext, actor)
	return version, err
}

// openVersion decrypts one version of an account.
func (v *Vault) openVersion(ctx context.Context, tx pgx.Tx, tenantID, id string, version int) (string, error) {
	var s sealed
	err := tx.QueryRow(ctx,
		`SELECT key_id, wrapped_key, nonce, ciphertext FROM vault_secret_versions WHERE vault_account_id = $1 AND version = $2`,
		id, version).Scan(&s.KeyID, &s.WrappedKey, &s.Nonce, &s.Ciphertext)
	if err != nil {
		return "", err
	}
	out, err := open(v.keys, s, versionAAD(tenantID, id, version))
	if err != nil {
		return "", fmt.Errorf("vault account %s version %d: %w", id, version, err)
	}
	return string(out), nil
}

// holds is true when c.identity_id, active, holds va's access right now:
// an unexpired assignment of its entitlement, directly or through a role.
const holds = `EXISTS (
	SELECT 1 FROM access_assignments x JOIN identities i ON i.id = x.identity_id
	WHERE x.identity_id = c.identity_id AND i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
	  AND (x.entitlement_id = va.entitlement_id
	       OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = va.entitlement_id)))`

// Holders returns the active identities who hold access to a vault account
// right now: an unexpired assignment of its entitlement, directly or through
// a business role.
func Holders(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	if !db.ValidUUID(id) {
		return nil, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT x.identity_id::text
		FROM vault_accounts va
		JOIN access_assignments x ON x.entitlement_id = va.entitlement_id
		   OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = va.entitlement_id)
		JOIN identities i ON i.id = x.identity_id
		WHERE va.id = $1 AND i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
		ORDER BY 1`, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vault_accounts WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, apperr.ErrNotFound
		}
	}
	return out, nil
}

// Lease is a check-out with the secret it handed out.
type Lease struct {
	Checkout
	Secret string `json:"secret"`
}

// Checkout hands the current secret of a vault account to identityID, who
// must hold access to it (the caller checks the policies), until the
// account's checkout time runs out.
func (v *Vault) Checkout(ctx context.Context, tx pgx.Tx, tenantID, identityID, id, reason string) (Lease, error) {
	return v.lease(ctx, tx, tenantID, identityID, id, reason, false)
}

// BreakGlassMinutes caps how long a break-glass check-out lasts.
const BreakGlassMinutes = 60

// BreakGlass opens a break-glass account for identityID, a responder (the
// caller checks the policies), without approval. The lease lasts at most
// BreakGlassMinutes, ignores exclusivity, alerts every admin and auditor,
// rotates the password when it ends and waits for an admin's review.
func (v *Vault) BreakGlass(ctx context.Context, tx pgx.Tx, tenantID, identityID, id, reason string) (Lease, error) {
	return v.lease(ctx, tx, tenantID, identityID, id, reason, true)
}

func (v *Vault) lease(ctx context.Context, tx pgx.Tx, tenantID, identityID, id, reason string, breakGlass bool) (Lease, error) {
	if !v.Enabled() {
		return Lease{}, ErrDisabled
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return Lease{}, apperr.Invalidf("reason must be between 1 and 1000 characters")
	}
	kind, _, canRotate, err := lock(ctx, tx, id)
	if err != nil {
		return Lease{}, err
	}
	canRotate = canRotate && kind == KindPassword
	var (
		failures         int
		minutes, version int
		exclusive, glass bool
		name, username   string
	)
	err = tx.QueryRow(ctx,
		`SELECT va.checkout_minutes, va.exclusive, va.break_glass, coalesce(s.version, 0), va.name, va.username,
		        va.rotation_failures
		 FROM vault_accounts va LEFT JOIN vault_secret_versions s ON s.vault_account_id = va.id AND s.status = 'current'
		 WHERE va.id = $1`, id).Scan(&minutes, &exclusive, &glass, &version, &name, &username, &failures)
	if err != nil {
		return Lease{}, err
	}
	if breakGlass && !glass {
		return Lease{}, apperr.Conflictf("this vault account is not a break-glass account")
	}
	if version == 0 {
		return Lease{}, apperr.Conflictf("this vault account has no secret yet")
	}
	if breakGlass {
		minutes = min(minutes, BreakGlassMinutes)
	}
	// In an emergency, someone else holding the account does not stop a
	// responder.
	if exclusive && !breakGlass {
		var who string
		var until time.Time
		err := tx.QueryRow(ctx,
			`SELECT i.display_name, c.expires_at FROM vault_checkouts c JOIN identities i ON i.id = c.identity_id
			 WHERE c.vault_account_id = $1 AND c.ended_at IS NULL AND c.expires_at > now() AND c.identity_id <> $2
			 ORDER BY c.expires_at DESC LIMIT 1`, id, identityID).Scan(&who, &until)
		if err == nil {
			return Lease{}, apperr.Conflictf("%s has this account checked out until %s", who, until.UTC().Format(time.RFC3339))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Lease{}, err
		}
	}
	secret, err := v.openVersion(ctx, tx, tenantID, id, version)
	if err != nil {
		return Lease{}, err
	}
	// One lease per person and account: checking out again extends it.
	if _, err := tx.Exec(ctx,
		`UPDATE vault_checkouts SET ended_at = now(), ended_by = 'checked_in'
		 WHERE vault_account_id = $1 AND identity_id = $2 AND ended_at IS NULL`, id, identityID); err != nil {
		return Lease{}, err
	}
	l := Lease{Secret: secret}
	// A check-out ends no later than the access it comes from, such as a
	// just-in-time activation.
	err = tx.QueryRow(ctx,
		`INSERT INTO vault_checkouts (tenant_id, vault_account_id, identity_id, version, reason, expires_at, break_glass, review_status)
		 VALUES ($1, $2, $3, $4, $5, least(now() + make_interval(mins => $6), CASE WHEN NOT $7 THEN (
		           SELECT max(coalesce(x.expires_at, 'infinity')) FROM access_assignments x, vault_accounts va
		           WHERE va.id = $2 AND x.identity_id = $3 AND (x.entitlement_id = va.entitlement_id
		                 OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = va.entitlement_id)))
		         END), $7, CASE WHEN $7 THEN 'pending' END)
		 RETURNING id, vault_account_id, identity_id, version, reason, checked_out_at, expires_at,
		           (SELECT display_name FROM identities WHERE id = $3), break_glass, coalesce(review_status, '')`,
		tenantID, id, identityID, version, reason, minutes, breakGlass).
		Scan(&l.ID, &l.VaultAccountID, &l.IdentityID, &l.Version, &l.Reason, &l.CheckedOutAt, &l.ExpiresAt, &l.IdentityName,
			&l.BreakGlass, &l.ReviewStatus)
	if err != nil {
		return Lease{}, err
	}
	l.AccountName = name
	event := "vault.checked_out"
	if breakGlass {
		event = "vault.break_glass_opened"
	}
	if err := db.Audit(ctx, tx, tenantID, db.IdentityActor(identityID), event, "vault_account", id,
		map[string]any{"checkout_id": l.ID, "version": version, "reason": reason, "expires_at": l.ExpiresAt.UTC(),
			"break_glass": breakGlass}); err != nil {
		return Lease{}, err
	}
	if !breakGlass {
		return l, nil
	}
	to, err := notify.Members(ctx, tx, authz.RoleAdmin, authz.RoleAuditor)
	if err != nil {
		return Lease{}, err
	}
	return l, notify.Enqueue(ctx, tx, tenantID, notify.BreakGlassUsed, to, map[string]any{
		"account": name, "username": username, "person": l.IdentityName, "reason": reason,
		"checked_out_at": l.CheckedOutAt.UTC().Format(time.RFC3339), "expires_at": l.ExpiresAt.UTC().Format(time.RFC3339),
		"rotates": canRotate && failures == 0})
}

// CheckoutOwner returns whose check-out id is, and its vault account.
func CheckoutOwner(ctx context.Context, tx pgx.Tx, id string) (identityID, accountID string, err error) {
	if !db.ValidUUID(id) {
		return "", "", apperr.ErrNotFound
	}
	err = tx.QueryRow(ctx, `SELECT identity_id, vault_account_id FROM vault_checkouts WHERE id = $1`, id).Scan(&identityID, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", apperr.ErrNotFound
	}
	return identityID, accountID, err
}

// Checkin ends a check-out: "checked_in" by its owner, "ended" by an admin.
// When the account rotates after use and nobody else has it out, its
// rotation becomes due.
func Checkin(ctx context.Context, tx pgx.Tx, tenantID, actor, checkoutID, endedBy string) error {
	_, account, err := CheckoutOwner(ctx, tx, checkoutID)
	if err != nil {
		return err
	}
	if _, _, _, err := lock(ctx, tx, account); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE vault_checkouts SET ended_at = now(), ended_by = $2 WHERE id = $1 AND ended_at IS NULL`, checkoutID, endedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Conflictf("this check-out has already ended")
	}
	if err := afterUse(ctx, tx, account); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "vault.checked_in", "vault_account", account,
		map[string]any{"checkout_id": checkoutID, "ended_by": endedBy})
}

// afterUse makes the rotation of a locked account due once its last open
// check-out ended, when it rotates after use or when a break-glass
// check-out revealed its current secret. A break-glass account whose
// application cannot set passwords cannot rotate, so nothing is due.
func afterUse(ctx context.Context, tx pgx.Tx, id string) error {
	kind, _, canRotate, err := lock(ctx, tx, id)
	if err != nil {
		return err
	}
	canRotate = canRotate && kind == KindPassword
	_, err = tx.Exec(ctx,
		`UPDATE vault_accounts va SET rotate_at = now()
		 WHERE va.id = $1 AND va.rotation_failures = 0
		   AND NOT EXISTS (SELECT 1 FROM vault_checkouts c WHERE c.vault_account_id = $1 AND c.ended_at IS NULL)
		   AND (va.rotate_after_checkin OR ($2 AND EXISTS (
		         SELECT 1 FROM vault_checkouts c JOIN vault_secret_versions s ON s.vault_account_id = c.vault_account_id
		         WHERE c.vault_account_id = $1 AND c.break_glass AND s.status = 'current' AND c.version = s.version)))`, id, canRotate)
	return err
}

// ExpireCheckouts ends the check-outs whose time ran out.
func ExpireCheckouts(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	// A check-out also ends once its holder no longer holds the access (an
	// activation ran out or was ended, a review revoked it, they left);
	// break-glass check-outs never needed it.
	rows, err := tx.Query(ctx,
		`UPDATE vault_checkouts c SET ended_at = now(), ended_by = 'expired'
		 FROM vault_accounts va
		 WHERE va.id = c.vault_account_id AND c.ended_at IS NULL
		   AND (c.expires_at <= now() OR (NOT c.break_glass AND NOT `+holds+`))
		 RETURNING c.id, c.vault_account_id, c.identity_id, c.expires_at > now()`)
	if err != nil {
		return 0, err
	}
	type ended struct {
		id, account, identity string
		unheld                bool
	}
	var list []ended
	for rows.Next() {
		var e ended
		if err := rows.Scan(&e.id, &e.account, &e.identity, &e.unheld); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var accounts []string
	for _, e := range list {
		details := map[string]any{"checkout_id": e.id, "identity_id": e.identity}
		if e.unheld {
			details["reason"] = "access ended"
		}
		if err := db.Audit(ctx, tx, tenantID, "system:vault", "vault.checkout_expired", "vault_account", e.account, details); err != nil {
			return 0, err
		}
		if !slices.Contains(accounts, e.account) {
			accounts = append(accounts, e.account)
		}
	}
	for _, a := range accounts {
		if err := afterUse(ctx, tx, a); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

const accountColumns = `va.id, va.name, va.username, coalesce(va.address, ''), coalesce(va.notes, ''), va.kind,
	coalesce(va.account_id::text, ''), coalesce(app.id::text, ''), coalesce(app.name, ''), coalesce(app.connector, ''),
	va.entitlement_id, e.name, e.risk, va.checkout_minutes, va.exclusive, coalesce(va.rotate_every_days, 0), va.rotate_after_checkin,
	va.rotate_at, va.rotation_failures, coalesce(va.last_rotation_error, ''), coalesce(s.version, 0), s.created_at, va.break_glass, va.created_at`

const accountFrom = ` FROM vault_accounts va
	JOIN entitlements e ON e.id = va.entitlement_id
	LEFT JOIN accounts a ON a.id = va.account_id
	LEFT JOIN applications app ON app.id = a.application_id
	LEFT JOIN vault_secret_versions s ON s.vault_account_id = va.id AND s.status = 'current'`

func scanAccount(row pgx.CollectableRow) (Account, error) {
	var a Account
	var connKind string
	err := row.Scan(&a.ID, &a.Name, &a.Username, &a.Address, &a.Notes, &a.Kind,
		&a.AccountID, &a.ApplicationID, &a.ApplicationName, &connKind,
		&a.EntitlementID, &a.EntitlementName, &a.Risk, &a.CheckoutMinutes, &a.Exclusive, &a.RotateEveryDays, &a.RotateAfterCheckin,
		&a.RotateAt, &a.RotationFailures, &a.LastRotationError, &a.Version, &a.SecretAt, &a.BreakGlass, &a.CreatedAt)
	a.CanRotate = a.AccountID != "" && a.Kind == KindPassword && connector.SetsPasswords(connKind)
	a.Checkouts = []Checkout{}
	a.Responders = []Responder{}
	return a, err
}

// List returns every vault account, or with holder set only the ones that
// identity holds access to, with their open check-outs.
func List(ctx context.Context, tx pgx.Tx, holder string) ([]Account, error) {
	rows, err := tx.Query(ctx, `SELECT `+accountColumns+accountFrom+`
		WHERE $1 = '' OR va.entitlement_id IN (
			SELECT coalesce(x.entitlement_id, re.entitlement_id) FROM access_assignments x
			LEFT JOIN role_entitlements re ON re.role_id = x.role_id
			WHERE x.identity_id::text = $1 AND (x.expires_at IS NULL OR x.expires_at > now()))
		ORDER BY va.name`, holder)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanAccount)
	if err != nil || len(list) == 0 {
		return list, err
	}
	if err := loadResponders(ctx, tx, list); err != nil {
		return nil, err
	}
	open, err := checkouts(ctx, tx, `c.ended_at IS NULL`, nil)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Account{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	for _, c := range open {
		if a, ok := byID[c.VaultAccountID]; ok {
			a.Checkouts = append(a.Checkouts, c)
		}
	}
	return list, nil
}

// Get returns one vault account with its open check-outs.
func Get(ctx context.Context, tx pgx.Tx, id string) (Account, error) {
	if !db.ValidUUID(id) {
		return Account{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+accountColumns+accountFrom+` WHERE va.id = $1`, id)
	if err != nil {
		return Account{}, err
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanAccount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, apperr.ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	one := []Account{a}
	if err := loadResponders(ctx, tx, one); err != nil {
		return Account{}, err
	}
	a = one[0]
	open, err := checkouts(ctx, tx, `c.ended_at IS NULL AND c.vault_account_id = $1`, []any{id})
	if err != nil {
		return Account{}, err
	}
	a.Checkouts = append(a.Checkouts, open...)
	return a, nil
}

func checkouts(ctx context.Context, tx pgx.Tx, where string, args []any) ([]Checkout, error) {
	rows, err := tx.Query(ctx,
		`SELECT c.id, c.vault_account_id, c.identity_id, i.display_name, c.version, c.reason, c.checked_out_at, c.expires_at,
		        c.ended_at, coalesce(c.ended_by, ''), c.break_glass, coalesce(c.review_status, ''), coalesce(c.reviewed_by, ''),
		        c.reviewed_at, coalesce(c.review_comment, ''), va.name
		 FROM vault_checkouts c JOIN identities i ON i.id = c.identity_id JOIN vault_accounts va ON va.id = c.vault_account_id
		 WHERE `+where+` ORDER BY c.checked_out_at DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Checkout, error) {
		var c Checkout
		err := row.Scan(&c.ID, &c.VaultAccountID, &c.IdentityID, &c.IdentityName, &c.Version, &c.Reason, &c.CheckedOutAt, &c.ExpiresAt,
			&c.EndedAt, &c.EndedBy, &c.BreakGlass, &c.ReviewStatus, &c.ReviewedBy, &c.ReviewedAt, &c.ReviewComment, &c.AccountName)
		return c, err
	})
}

// History is a vault account's versions and its latest check-outs.
type History struct {
	Versions []Version `json:"versions"`
	// Checkouts are the latest check-outs, open or ended.
	Checkouts []Checkout `json:"recent_checkouts"`
}

// GetHistory returns the versions of a vault account, newest first, and its
// last 100 check-outs.
func GetHistory(ctx context.Context, tx pgx.Tx, id string) (History, error) {
	if _, err := Get(ctx, tx, id); err != nil {
		return History{}, err
	}
	rows, err := tx.Query(ctx,
		`SELECT version, status, source, key_id, created_by, created_at FROM vault_secret_versions
		 WHERE vault_account_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return History{}, err
	}
	var h History
	h.Versions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Version])
	if err != nil {
		return History{}, err
	}
	h.Checkouts, err = checkouts(ctx, tx, `c.vault_account_id = $1`, []any{id})
	if h.Versions == nil {
		h.Versions = []Version{}
	}
	if h.Checkouts == nil {
		h.Checkouts = []Checkout{}
	}
	return h, err
}
