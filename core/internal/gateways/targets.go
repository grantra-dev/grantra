// Package gateways gives people access to Kubernetes clusters and
// PostgreSQL and MySQL databases through the Grantra gateway, without
// credentials of their own there:
//
//   - Whoever holds access to a target gets a token for it that lasts the
//     target's maximum and never past the access. They connect to the
//     gateway with it: as the password of the database user named like the
//     target, or as the bearer token of a kubeconfig.
//   - The gateway logs in to the target with an account kept in the vault
//     (a database login, or a Kubernetes service account allowed to
//     impersonate), so nobody sees that password, and acts on the cluster
//     as the person (impersonated, with the target's groups).
//   - It checks access when people connect and every 30 seconds after, and
//     logs every query and every cluster request for the console.
//
// Who may use a target is an entitlement in the "Databases and clusters"
// application, created with the target, so requests, approvals, reviews,
// separation of duties, just-in-time activation and expiry govern it like
// any other access.
//
// The proxies themselves are in the proxy package; this package keeps the
// targets, tokens, sessions and the CA of the gateway's TLS certificates.
package gateways

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// ApplicationName is the application whose entitlements give access to
// gateway targets.
const ApplicationName = "Databases and clusters"

// Target kinds.
const (
	Kubernetes = "kubernetes"
	Postgres   = "postgres"
	MySQL      = "mysql"
)

// TLS modes: how the gateway talks to a target.
const (
	TLSVerify  = "verify"
	TLSRequire = "require"
	TLSDisable = "disable"
)

// Target is a cluster or database people reach through the gateway.
type Target struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Database string `json:"database,omitempty"`
	TLSMode  string `json:"tls_mode"`
	// CACertificate verifies the target instead of the system's CAs.
	CACertificate string `json:"ca_certificate,omitempty"`
	// VaultAccountID is the account the gateway logs in with.
	VaultAccountID   string   `json:"vault_account_id,omitempty"`
	VaultAccountName string   `json:"vault_account_name,omitempty"`
	KubeGroups       []string `json:"kube_groups"`
	Notes            string   `json:"notes,omitempty"`
	EntitlementID    string   `json:"entitlement_id"`
	EntitlementName  string   `json:"entitlement_name"`
	Risk             string   `json:"risk"`
	// MaxTokenHours caps how long a token lasts; it never outlasts the
	// access it comes from either.
	MaxTokenHours int       `json:"max_token_hours"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Held says whether the person asking holds access to it. Set by the
	// console.
	Held bool `json:"held"`
}

// Settings are what can change on a target after it is created. Fields
// left out (nil) keep their value.
type Settings struct {
	Address        *string  `json:"address"`
	Database       *string  `json:"database"`
	TLSMode        *string  `json:"tls_mode"`
	CACertificate  *string  `json:"ca_certificate"`
	VaultAccountID *string  `json:"vault_account_id"`
	KubeGroups     []string `json:"kube_groups"`
	MaxTokenHours  *int     `json:"max_token_hours"`
	Notes          *string  `json:"notes"`
}

// Input creates a target.
type Input struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Risk string `json:"risk"`
	Settings
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	databasePattern = regexp.MustCompile(`^[A-Za-z0-9_$][A-Za-z0-9_$.-]{0,62}$`)
)

// config is a target's settings once merged and checked.
type config struct {
	address, database, tlsMode, caCert, vaultAccountID, notes string
	kubeGroups                                                []string
	maxTokenHours                                             int
}

func (c *config) apply(s Settings) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&c.address, s.Address)
	set(&c.database, s.Database)
	set(&c.tlsMode, s.TLSMode)
	set(&c.caCert, s.CACertificate)
	set(&c.vaultAccountID, s.VaultAccountID)
	set(&c.notes, s.Notes)
	if s.KubeGroups != nil {
		c.kubeGroups = s.KubeGroups
	}
	if s.MaxTokenHours != nil {
		c.maxTokenHours = *s.MaxTokenHours
	}
}

func defaultPort(kind string) string {
	if kind == MySQL {
		return "3306"
	}
	return "5432"
}

// check validates c for a target of kind named name.
func (c *config) check(ctx context.Context, tx pgx.Tx, kind, name string) error {
	if kind == Kubernetes {
		u, err := url.Parse(c.address)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return apperr.Invalidf("address must be the cluster API server's https URL, such as https://10.0.0.1:6443")
		}
		c.address = strings.TrimSuffix(u.String(), "/")
		if c.database != "" {
			return apperr.Invalidf("a cluster has no database")
		}
		if c.tlsMode == TLSDisable {
			return apperr.Invalidf("the gateway always talks TLS to a cluster: tls_mode must be verify or require")
		}
		if c.kubeGroups == nil {
			c.kubeGroups = []string{"grantra:" + name}
		}
		if len(c.kubeGroups) > 20 {
			return apperr.Invalidf("kube_groups takes at most 20 groups")
		}
		seen := map[string]bool{}
		for i, g := range c.kubeGroups {
			g = strings.TrimSpace(g)
			if g == "" || len(g) > 200 || strings.ContainsFunc(g, unicode.IsControl) {
				return apperr.Invalidf("each group in kube_groups must be 1 to 200 characters")
			}
			// system:masters skips RBAC altogether; system groups belong to
			// the cluster.
			if strings.HasPrefix(g, "system:") {
				return apperr.Invalidf("kube_groups cannot hold the cluster's system: groups, such as system:masters; bind a group of yours to the role it needs")
			}
			if seen[g] {
				return apperr.Invalidf("kube_groups lists %q twice", g)
			}
			seen[g] = true
			c.kubeGroups[i] = g
		}
	} else {
		addr := c.address
		if addr != "" && !strings.Contains(addr, ":") {
			addr += ":" + defaultPort(kind)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host == "" || strings.ContainsAny(host, " /@") {
			return apperr.Invalidf("address must be a host name or IP, with an optional :port (default %s)", defaultPort(kind))
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return apperr.Invalidf("address has an invalid port")
		}
		c.address = net.JoinHostPort(host, port)
		if c.database != "" && !databasePattern.MatchString(c.database) {
			return apperr.Invalidf("database must be a database name of at most 63 letters, digits and _ $ . -")
		}
		if len(c.kubeGroups) > 0 {
			return apperr.Invalidf("kube_groups are for clusters only")
		}
		c.kubeGroups = []string{}
	}
	if c.tlsMode == "" {
		c.tlsMode = TLSVerify
	}
	if c.tlsMode != TLSVerify && c.tlsMode != TLSRequire && c.tlsMode != TLSDisable {
		return apperr.Invalidf("tls_mode must be verify, require or disable")
	}
	if c.caCert != "" {
		if _, err := certPool(c.caCert); err != nil {
			return err
		}
	}
	if c.vaultAccountID == "" {
		if kind != Kubernetes {
			return apperr.Invalidf("vault_account_id must name the vault account the gateway logs in to the database with")
		}
	} else {
		invalid := apperr.Invalidf("vault_account_id does not name a vault account")
		if !db.ValidUUID(c.vaultAccountID) {
			return invalid
		}
		var vkind string
		err := tx.QueryRow(ctx, `SELECT kind FROM vault_accounts WHERE id = $1`, c.vaultAccountID).Scan(&vkind)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid
		}
		if err != nil {
			return err
		}
		if vkind != vault.KindPassword {
			return apperr.Invalidf("the gateway logs in with a password account (a database password or a service account token), not an SSH key")
		}
	}
	if c.maxTokenHours == 0 {
		c.maxTokenHours = 8
	}
	if c.maxTokenHours < 1 || c.maxTokenHours > 24 {
		return apperr.Invalidf("max_token_hours must be between 1 and 24")
	}
	if len(c.notes) > 2000 {
		return apperr.Invalidf("notes must be at most 2000 characters")
	}
	return nil
}

// certPool parses PEM CA certificates, at most 64 KB of them.
func certPool(pemText string) (*x509.CertPool, error) {
	invalid := apperr.Invalidf("ca_certificate must be one or more PEM certificates (-----BEGIN CERTIFICATE-----)")
	if len(pemText) > 64<<10 {
		return nil, invalid
	}
	pool := x509.NewCertPool()
	rest := []byte(pemText)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, invalid
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, invalid
		}
		pool.AddCert(cert)
		n++
	}
	if n == 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, invalid
	}
	return pool, nil
}

// CertPool returns the pool a target is verified with: its own CA
// certificates, or nil for the system's.
func (t Target) CertPool() (*x509.CertPool, error) {
	if t.CACertificate == "" {
		return nil, nil
	}
	return certPool(t.CACertificate)
}

func describe(kind, name, address string) (entName, desc string) {
	switch kind {
	case Kubernetes:
		return "Use the " + name + " cluster", "Kubernetes at " + address + " through the Grantra gateway, with a short-lived token. Every request is logged."
	case MySQL:
		return "Connect to the " + name + " database", "MySQL at " + address + " through the Grantra gateway, with a short-lived token. Every query is logged."
	default:
		return "Connect to the " + name + " database", "PostgreSQL at " + address + " through the Grantra gateway, with a short-lived token. Every query is logged."
	}
}

// Create adds a target, and the entitlement that gives access to it.
// Nobody holds it for good by default: people are made eligible and
// activate it, or request it for a time. Admins may change that on the
// entitlement.
func Create(ctx context.Context, tx pgx.Tx, tenantID, actor string, in Input) (Target, error) {
	name := strings.TrimSpace(in.Name)
	if !namePattern.MatchString(name) {
		return Target{}, apperr.Invalidf("name must be 1 to 63 lowercase letters, digits, dots, dashes or underscores, starting with a letter or digit: people connect with it")
	}
	if !slices.Contains([]string{Kubernetes, Postgres, MySQL}, in.Kind) {
		return Target{}, apperr.Invalidf("kind must be kubernetes, postgres or mysql")
	}
	if in.Risk == "" {
		in.Risk = "high"
	}
	if in.Risk != "high" && in.Risk != "critical" {
		return Target{}, apperr.Invalidf("risk must be high or critical: access to clusters and databases is privileged")
	}
	var c config
	c.apply(in.Settings)
	if err := c.check(ctx, tx, in.Kind, name); err != nil {
		return Target{}, err
	}
	appID, err := ensureApplication(ctx, tx, tenantID)
	if err != nil {
		return Target{}, err
	}
	entName, desc := describe(in.Kind, name, c.address)
	ent, err := catalog.CreateEntitlement(ctx, tx, tenantID, appID, catalog.EntitlementInput{
		NativeID: "gateway:" + name, Name: entName, Description: desc, Risk: in.Risk})
	if err != nil {
		var cf *apperr.Conflict
		if errors.As(err, &cf) {
			return Target{}, apperr.Conflictf("a target named %q already exists", name)
		}
		return Target{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET jit_only = true WHERE id = $1`, ent.ID); err != nil {
		return Target{}, err
	}
	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO gateway_targets (tenant_id, name, kind, address, database, tls_mode, ca_certificate, vault_account_id,
		                              kube_groups, notes, entitlement_id, max_token_hours, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT DO NOTHING RETURNING id`,
		tenantID, name, in.Kind, c.address, db.Nullable(c.database), c.tlsMode, db.Nullable(c.caCert), db.Nullable(c.vaultAccountID),
		c.kubeGroups, db.Nullable(c.notes), ent.ID, c.maxTokenHours, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.Conflictf("a target named %q already exists", name)
	}
	if err != nil {
		return Target{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "gateway.target_created", "gateway_target", id,
		map[string]any{"name": name, "kind": in.Kind, "address": c.address, "tls_mode": c.tlsMode,
			"vault_account_id": c.vaultAccountID, "kube_groups": c.kubeGroups, "entitlement_id": ent.ID}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// ensureApplication returns the "Databases and clusters" application,
// creating it the first time. It is governed by hand: there is nothing to
// read or provision, only access to grant.
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
		return "", apperr.Conflictf("the application %q has a connector; gateway access needs that name for its own application, so rename the other one", ApplicationName)
	}
	return id, nil
}

// Update changes a target's settings. Its name and kind stay: they are what
// the access means.
func Update(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, s Settings) (Target, error) {
	cur, err := lockTarget(ctx, tx, id)
	if err != nil {
		return Target{}, err
	}
	c := config{address: cur.Address, database: cur.Database, tlsMode: cur.TLSMode, caCert: cur.CACertificate,
		vaultAccountID: cur.VaultAccountID, notes: cur.Notes, kubeGroups: cur.KubeGroups, maxTokenHours: cur.MaxTokenHours}
	c.apply(s)
	if err := c.check(ctx, tx, cur.Kind, cur.Name); err != nil {
		return Target{}, err
	}
	_, err = tx.Exec(ctx,
		`UPDATE gateway_targets SET address = $2, database = $3, tls_mode = $4, ca_certificate = $5, vault_account_id = $6,
		        kube_groups = $7, notes = $8, max_token_hours = $9, updated_at = now()
		 WHERE id = $1`, id, c.address, db.Nullable(c.database), c.tlsMode, db.Nullable(c.caCert), db.Nullable(c.vaultAccountID),
		c.kubeGroups, db.Nullable(c.notes), c.maxTokenHours)
	if err != nil {
		return Target{}, err
	}
	_, desc := describe(cur.Kind, cur.Name, c.address)
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET description = $2 WHERE id = $1`, cur.EntitlementID, desc); err != nil {
		return Target{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "gateway.target_updated", "gateway_target", id,
		map[string]any{"address": c.address, "database": c.database, "tls_mode": c.tlsMode,
			"ca_certificate_changed": c.caCert != cur.CACertificate, "vault_account_id": c.vaultAccountID,
			"kube_groups": c.kubeGroups, "max_token_hours": c.maxTokenHours}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// Delete removes a target and the entitlement that gave access to it, with
// every assignment, request and token of it. Sessions and their logs stay;
// running ones end at the gateway's next check.
func Delete(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	cur, err := lockTarget(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_targets WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE id = $1`, cur.EntitlementID); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "gateway.target_deleted", "gateway_target", id,
		map[string]any{"name": cur.Name, "kind": cur.Kind})
}

func lockTarget(ctx context.Context, tx pgx.Tx, id string) (Target, error) {
	if !db.ValidUUID(id) {
		return Target{}, apperr.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM gateway_targets WHERE id = $1 FOR UPDATE`, id); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

const targetSelect = `SELECT t.id, t.name, t.kind, t.address, coalesce(t.database, ''), t.tls_mode, coalesce(t.ca_certificate, ''),
	coalesce(t.vault_account_id::text, ''), coalesce(va.name, ''), t.kube_groups, coalesce(t.notes, ''), t.entitlement_id, e.name,
	e.risk, t.max_token_hours, t.created_at, t.updated_at
	FROM gateway_targets t JOIN entitlements e ON e.id = t.entitlement_id
	LEFT JOIN vault_accounts va ON va.id = t.vault_account_id`

func scanTarget(row pgx.CollectableRow) (Target, error) {
	var t Target
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Address, &t.Database, &t.TLSMode, &t.CACertificate, &t.VaultAccountID,
		&t.VaultAccountName, &t.KubeGroups, &t.Notes, &t.EntitlementID, &t.EntitlementName, &t.Risk, &t.MaxTokenHours,
		&t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// heldBy is true when $1, an active identity, holds the target t's access
// right now: an unexpired assignment of its entitlement, directly or
// through a role.
const heldBy = `EXISTS (
	SELECT 1 FROM access_assignments x JOIN identities i ON i.id = x.identity_id
	WHERE x.identity_id::text = $1 AND i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
	  AND (x.entitlement_id = t.entitlement_id
	       OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = t.entitlement_id)))`

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
	rows, err := tx.Query(ctx, targetSelect+` WHERE t.id = $1`, id)
	if err != nil {
		return Target{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTarget)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.ErrNotFound
	}
	return t, err
}

// Holders returns the active identities who hold access to a target right
// now.
func Holders(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	if _, err := Get(ctx, tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT x.identity_id::text
		FROM gateway_targets t
		JOIN access_assignments x ON x.entitlement_id = t.entitlement_id
		   OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = t.entitlement_id)
		JOIN identities i ON i.id = x.identity_id
		WHERE t.id = $1 AND i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
		ORDER BY 1`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
