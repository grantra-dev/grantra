package sshaccess

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// ApplicationName is the application whose entitlements give access to SSH
// targets.
const ApplicationName = "SSH servers"

// Target is a server people log in to with a Grantra certificate.
type Target struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	Login           string `json:"login"`
	Notes           string `json:"notes,omitempty"`
	EntitlementID   string `json:"entitlement_id"`
	EntitlementName string `json:"entitlement_name"`
	Risk            string `json:"risk"`
	// MaxCertificateHours caps how long a certificate lasts; it never
	// outlasts the access it comes from either.
	MaxCertificateHours int `json:"max_certificate_hours"`
	// ViaGateway targets are reached through the gateway, which records
	// every session. Certificates for them do not log in to the server
	// directly.
	ViaGateway bool `json:"via_gateway"`
	// HostKey is the server's host key the gateway learned, empty until its
	// first connection.
	HostKey   string    `json:"host_key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Held says whether the person asking holds access to it. Set by the
	// console.
	Held bool `json:"held"`
}

// Settings are what can change on a target after it is created.
type Settings struct {
	Address             string `json:"address"`
	Notes               string `json:"notes"`
	MaxCertificateHours int    `json:"max_certificate_hours"`
	ViaGateway          *bool  `json:"via_gateway"`
	// ForgetHostKey clears the learned host key, after the server's key
	// changed on purpose.
	ForgetHostKey bool `json:"forget_host_key"`
}

// Input creates a target.
type Input struct {
	Name  string `json:"name"`
	Login string `json:"login"`
	Risk  string `json:"risk"`
	Settings
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,31}$`)
)

func (s *Settings) check() error {
	addr := strings.TrimSpace(s.Address)
	if addr != "" && !strings.Contains(addr, ":") {
		addr += ":22"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, " /@") {
		return apperr.Invalidf("address must be a host name or IP, with an optional :port (default 22)")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return apperr.Invalidf("address has an invalid port")
	}
	s.Address = net.JoinHostPort(host, port)
	s.Notes = strings.TrimSpace(s.Notes)
	if len(s.Notes) > 2000 {
		return apperr.Invalidf("notes must be at most 2000 characters")
	}
	if s.MaxCertificateHours == 0 {
		s.MaxCertificateHours = 8
	}
	if s.MaxCertificateHours < 1 || s.MaxCertificateHours > 24 {
		return apperr.Invalidf("max_certificate_hours must be between 1 and 24")
	}
	return nil
}

// Create adds a target, and the entitlement that gives access to it.
// Nobody holds it for good by default: people are made eligible and
// activate it, or request it for a time. Admins may change that on the
// entitlement.
func Create(ctx context.Context, tx pgx.Tx, tenantID, actor string, in Input) (Target, error) {
	name := strings.TrimSpace(in.Name)
	if !namePattern.MatchString(name) {
		return Target{}, apperr.Invalidf("name must be 1 to 63 lowercase letters, digits, dots, dashes or underscores, starting with a letter or digit: people type it to connect (ssh <name>@gateway)")
	}
	login := strings.TrimSpace(in.Login)
	if !loginPattern.MatchString(login) {
		return Target{}, apperr.Invalidf("login must be a user name on the server, such as root or deploy")
	}
	if in.Risk == "" {
		in.Risk = "high"
	}
	if in.Risk != "high" && in.Risk != "critical" {
		return Target{}, apperr.Invalidf("risk must be high or critical: SSH access to servers is privileged")
	}
	if err := in.Settings.check(); err != nil {
		return Target{}, err
	}
	viaGateway := in.ViaGateway == nil || *in.ViaGateway

	appID, err := catalog.EnsureApplication(ctx, tx, tenantID, ApplicationName, "SSH access")
	if err != nil {
		return Target{}, err
	}
	ent, err := catalog.CreateEntitlement(ctx, tx, tenantID, appID, catalog.EntitlementInput{
		NativeID:    "ssh:" + name,
		Name:        "Log in to " + name + " as " + login,
		Description: "SSH to " + in.Address + " as " + login + " with a short-lived Grantra certificate.",
		Risk:        in.Risk})
	if err != nil {
		var c *apperr.Conflict
		if errors.As(err, &c) {
			return Target{}, apperr.Conflictf("an SSH target named %q already exists", name)
		}
		return Target{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET jit_only = true WHERE id = $1`, ent.ID); err != nil {
		return Target{}, err
	}
	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO ssh_targets (tenant_id, name, address, login, notes, entitlement_id, max_certificate_hours, via_gateway, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING RETURNING id`,
		tenantID, name, in.Address, login, db.Nullable(in.Notes), ent.ID, in.MaxCertificateHours, viaGateway, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.Conflictf("an SSH target named %q already exists", name)
	}
	if err != nil {
		return Target{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "ssh.target_created", "ssh_target", id,
		map[string]any{"name": name, "address": in.Address, "login": login, "entitlement_id": ent.ID, "via_gateway": viaGateway}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// Update changes a target's settings. Its name and login stay: they are
// what the access means.
func Update(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, s Settings) (Target, error) {
	cur, err := lockTarget(ctx, tx, id)
	if err != nil {
		return Target{}, err
	}
	if strings.TrimSpace(s.Address) == "" {
		s.Address = cur.Address
	}
	if err := s.check(); err != nil {
		return Target{}, err
	}
	viaGateway := cur.ViaGateway
	if s.ViaGateway != nil {
		viaGateway = *s.ViaGateway
	}
	// A new address is another server: its host key is learned again.
	forget := s.ForgetHostKey || s.Address != cur.Address
	_, err = tx.Exec(ctx,
		`UPDATE ssh_targets SET address = $2, notes = $3, max_certificate_hours = $4, via_gateway = $5,
		        host_key = CASE WHEN $6 THEN NULL ELSE host_key END, updated_at = now()
		 WHERE id = $1`, id, s.Address, db.Nullable(s.Notes), s.MaxCertificateHours, viaGateway, forget)
	if err != nil {
		return Target{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET description = $2 WHERE id = $1`, cur.EntitlementID,
		"SSH to "+s.Address+" as "+cur.Login+" with a short-lived Grantra certificate."); err != nil {
		return Target{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "ssh.target_updated", "ssh_target", id,
		map[string]any{"address": s.Address, "max_certificate_hours": s.MaxCertificateHours, "via_gateway": viaGateway,
			"host_key_forgotten": forget && cur.HostKey != ""}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// Delete removes a target and the entitlement that gave access to it, with
// every assignment and request of it. Certificates already signed for it
// stop working through the gateway; recordings stay.
func Delete(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	cur, err := lockTarget(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ssh_targets WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE id = $1`, cur.EntitlementID); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "ssh.target_deleted", "ssh_target", id, map[string]any{"name": cur.Name})
}

func lockTarget(ctx context.Context, tx pgx.Tx, id string) (Target, error) {
	if !db.ValidUUID(id) {
		return Target{}, apperr.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM ssh_targets WHERE id = $1 FOR UPDATE`, id); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

const targetSelect = `SELECT t.id, t.name, t.address, t.login, coalesce(t.notes, ''), t.entitlement_id, e.name, e.risk,
	t.max_certificate_hours, t.via_gateway, coalesce(t.host_key, ''), t.created_at
	FROM ssh_targets t JOIN entitlements e ON e.id = t.entitlement_id`

func scanTarget(row pgx.CollectableRow) (Target, error) {
	var t Target
	err := row.Scan(&t.ID, &t.Name, &t.Address, &t.Login, &t.Notes, &t.EntitlementID, &t.EntitlementName, &t.Risk,
		&t.MaxCertificateHours, &t.ViaGateway, &t.HostKey, &t.CreatedAt)
	return t, err
}

// heldBy is true when $1, an active identity (empty for nobody), holds
// the target t's access right now.
var heldBy = catalog.HeldSQL(`nullif($1, '')::uuid`, `t.entitlement_id`)

// List returns every target, or with holder set only the ones that identity
// holds access to right now.
func List(ctx context.Context, tx pgx.Tx, holder string) ([]Target, error) {
	rows, err := tx.Query(ctx, targetSelect+` WHERE $1 = '' OR `+heldBy+` ORDER BY t.name`, holder)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTarget)
}

// Get returns one target.
func Get(ctx context.Context, tx pgx.Tx, id string) (Target, error) {
	if !db.ValidUUID(id) {
		return Target{}, apperr.ErrNotFound
	}
	return db.QueryOne(ctx, tx, scanTarget, targetSelect+` WHERE t.id = $1`, id)
}

// Holders returns the active identities who hold access to a target right
// now.
func Holders(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	t, err := Get(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return catalog.Holders(ctx, tx, t.EntitlementID)
}
