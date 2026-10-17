// Package gateways gives people access to Kubernetes clusters, PostgreSQL
// and MySQL databases, remote desktops (RDP) and cloud consoles (AWS, Azure,
// Google Cloud) through Grantra, without credentials of their own there:
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
//   - A desktop opens in the browser: the console relays it to guacd, which
//     logs in with the vault account, and records the screen.
//   - A cloud console signs the person in with access Grantra grants there
//     for the session only: temporary credentials of an AWS role, an Azure
//     role assignment or an expiring Google Cloud IAM binding (see the
//     cloud package).
//
// Who may use a target is an entitlement in the "Remote access"
// application, created with the target, so requests, approvals, reviews,
// separation of duties, just-in-time activation and expiry govern it like
// any other access.
//
// The proxies themselves are in the proxy package, the cloud consoles in
// the cloud package; this package keeps the targets, tokens, sessions and
// the CA of the gateway's TLS certificates.
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
const ApplicationName = "Remote access"

// Target kinds.
const (
	Kubernetes = "kubernetes"
	Postgres   = "postgres"
	MySQL      = "mysql"
	RDP        = "rdp"
	AWS        = "aws"
	Azure      = "azure"
	GCP        = "gcp"
)

// Kinds are every target kind.
var Kinds = []string{Kubernetes, Postgres, MySQL, RDP, AWS, Azure, GCP}

// Cloud says whether kind is a cloud console.
func Cloud(kind string) bool { return kind == AWS || kind == Azure || kind == GCP }

// Clipboard settings of a desktop.
const (
	ClipboardBoth  = "both"
	ClipboardPaste = "paste"
	ClipboardNone  = "none"
)

// TLS modes: how the gateway talks to a target.
const (
	TLSVerify  = "verify"
	TLSRequire = "require"
	TLSDisable = "disable"
)

// Target is a cluster, database, desktop or cloud console people reach
// through the gateway.
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
	// Clipboard is which way the clipboard works on a desktop.
	Clipboard string `json:"clipboard,omitempty"`
	// CloudRole is the role a cloud console signs people in with on Azure
	// and Google Cloud (on AWS the address is the role).
	CloudRole string `json:"cloud_role,omitempty"`
	// CloudDirectory is the Azure tenant the gateway signs in to.
	CloudDirectory  string `json:"cloud_directory,omitempty"`
	Notes           string `json:"notes,omitempty"`
	EntitlementID   string `json:"entitlement_id"`
	EntitlementName string `json:"entitlement_name"`
	Risk            string `json:"risk"`
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
	Clipboard      *string  `json:"clipboard"`
	CloudRole      *string  `json:"cloud_role"`
	CloudDirectory *string  `json:"cloud_directory"`
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
	clipboard, cloudRole, cloudDirectory                      string
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
	set(&c.clipboard, s.Clipboard)
	set(&c.cloudRole, s.CloudRole)
	set(&c.cloudDirectory, s.CloudDirectory)
	if s.KubeGroups != nil {
		c.kubeGroups = s.KubeGroups
	}
	if s.MaxTokenHours != nil {
		c.maxTokenHours = *s.MaxTokenHours
	}
}

func defaultPort(kind string) string {
	switch kind {
	case MySQL:
		return "3306"
	case RDP:
		return "3389"
	}
	return "5432"
}

var (
	awsRolePattern    = regexp.MustCompile(`^arn:aws(-us-gov|-cn)?:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	gcpProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	gcpRolePattern    = regexp.MustCompile(`^(roles|projects/[a-z][a-z0-9-]{4,28}[a-z0-9]/roles|organizations/[0-9]{1,20}/roles)/[A-Za-z0-9_.]{1,64}$`)
	azureScopePattern = regexp.MustCompile(`(?i)^/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(/resourceGroups/[-\w.()]{1,90})?$`)
	azureDirPattern   = regexp.MustCompile(`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[A-Za-z0-9][A-Za-z0-9.-]{0,253}\.[A-Za-z]{2,63})$`)
	azureRolePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ()._/-]{0,127}$`)
)

// check validates c for a target of kind named name.
func (c *config) check(ctx context.Context, tx pgx.Tx, kind, name string) error {
	for _, f := range []struct {
		field     string
		set, fits bool
		kinds     string
	}{
		{"database", c.database != "", kind == Postgres || kind == MySQL, "databases"},
		{"kube_groups", len(c.kubeGroups) > 0, kind == Kubernetes, "clusters"},
		{"clipboard", c.clipboard != "", kind == RDP, "desktops"},
		{"cloud_role", c.cloudRole != "", kind == Azure || kind == GCP, "Azure and Google Cloud"},
		{"cloud_directory", c.cloudDirectory != "", kind == Azure, "Azure"},
		{"ca_certificate", c.caCert != "", kind == Kubernetes || kind == Postgres || kind == MySQL, "clusters and databases"},
	} {
		if f.set && !f.fits {
			return apperr.Invalidf("%s is for %s only", f.field, f.kinds)
		}
	}
	var err error
	switch kind {
	case Kubernetes:
		err = c.checkCluster(name)
	case Postgres, MySQL, RDP:
		err = c.checkHost(kind)
	default:
		err = c.checkCloud(kind)
	}
	if err != nil {
		return err
	}
	if kind != Kubernetes {
		c.kubeGroups = []string{}
	}
	if c.tlsMode == "" {
		c.tlsMode = TLSVerify
	}
	if c.tlsMode != TLSVerify && c.tlsMode != TLSRequire && c.tlsMode != TLSDisable {
		return apperr.Invalidf("tls_mode must be verify, require or disable")
	}
	if Cloud(kind) && c.tlsMode != TLSVerify {
		return apperr.Invalidf("the gateway always verifies a cloud's certificates: tls_mode must be verify")
	}
	if c.caCert != "" {
		if _, err := certPool(c.caCert); err != nil {
			return err
		}
	}
	if err := c.checkVaultAccount(ctx, tx, kind); err != nil {
		return err
	}
	maxHours := 24
	if kind == AWS {
		// The longest an AWS role session lasts.
		maxHours = 12
	}
	if c.maxTokenHours == 0 {
		c.maxTokenHours = 8
		if kind == AWS {
			// The longest roles allow unless told otherwise.
			c.maxTokenHours = 1
		}
	}
	if c.maxTokenHours < 1 || c.maxTokenHours > maxHours {
		return apperr.Invalidf("max_token_hours must be between 1 and %d", maxHours)
	}
	if len(c.notes) > 2000 {
		return apperr.Invalidf("notes must be at most 2000 characters")
	}
	return nil
}

func (c *config) checkCluster(name string) error {
	u, err := url.Parse(c.address)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return apperr.Invalidf("address must be the cluster API server's https URL, such as https://10.0.0.1:6443")
	}
	c.address = strings.TrimSuffix(u.String(), "/")
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
	return nil
}

// checkHost checks a database's or desktop's address and settings.
func (c *config) checkHost(kind string) error {
	addr := c.address
	if addr != "" && !strings.Contains(addr, ":") {
		addr += ":" + defaultPort(kind)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, " /@,;") || strings.ContainsFunc(host, unicode.IsControl) {
		return apperr.Invalidf("address must be a host name or IP, with an optional :port (default %s)", defaultPort(kind))
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return apperr.Invalidf("address has an invalid port")
	}
	c.address = net.JoinHostPort(host, port)
	if c.database != "" && !databasePattern.MatchString(c.database) {
		return apperr.Invalidf("database must be a database name of at most 63 letters, digits and _ $ . -")
	}
	if kind == RDP {
		if c.tlsMode == TLSDisable {
			return apperr.Invalidf("a desktop is always reached over TLS: tls_mode must be verify or require")
		}
		if c.clipboard == "" {
			c.clipboard = ClipboardPaste
		}
		if c.clipboard != ClipboardBoth && c.clipboard != ClipboardPaste && c.clipboard != ClipboardNone {
			return apperr.Invalidf("clipboard must be both, paste or none")
		}
	}
	return nil
}

// checkCloud checks a cloud console's address and role.
func (c *config) checkCloud(kind string) error {
	switch kind {
	case AWS:
		if !awsRolePattern.MatchString(c.address) {
			return apperr.Invalidf("address must be the ARN of the IAM role people get, such as arn:aws:iam::123456789012:role/ReadOnly")
		}
	case GCP:
		if !gcpProjectPattern.MatchString(c.address) {
			return apperr.Invalidf("address must be the Google Cloud project ID, such as shop-prod-123")
		}
		if !gcpRolePattern.MatchString(c.cloudRole) {
			return apperr.Invalidf("cloud_role must be the role people get, such as roles/compute.viewer or projects/<project>/roles/<custom role>")
		}
		// Google Cloud refuses conditions, and so an end, on basic roles.
		if slices.Contains([]string{"roles/owner", "roles/editor", "roles/viewer"}, c.cloudRole) {
			return apperr.Invalidf("Google Cloud cannot make %s expire: pick a predefined or custom role instead", c.cloudRole)
		}
	case Azure:
		if !azureScopePattern.MatchString(c.address) {
			return apperr.Invalidf("address must be the scope people get the role on: /subscriptions/<id> or /subscriptions/<id>/resourceGroups/<name>")
		}
		if !azureRolePattern.MatchString(c.cloudRole) {
			return apperr.Invalidf("cloud_role must be an Azure role's name (such as Reader) or id")
		}
		if c.cloudDirectory != "" && !azureDirPattern.MatchString(c.cloudDirectory) {
			return apperr.Invalidf("cloud_directory must be the Microsoft Entra tenant's id or domain")
		}
		if c.vaultAccountID != "" && c.cloudDirectory == "" {
			return apperr.Invalidf("cloud_directory must name the Microsoft Entra tenant the vault account's app is registered in")
		}
	}
	return nil
}

// checkVaultAccount checks the account the gateway logs in with. Clusters
// and Azure and Google Cloud consoles may go without one: the gateway then
// uses its own identity there.
func (c *config) checkVaultAccount(ctx context.Context, tx pgx.Tx, kind string) error {
	if c.vaultAccountID == "" {
		switch kind {
		case Kubernetes, Azure, GCP:
			return nil
		case AWS:
			return apperr.Invalidf("vault_account_id must name the vault account with the access key of the IAM user that assumes the role")
		case RDP:
			return apperr.Invalidf("vault_account_id must name the vault account the gateway logs in to the desktop with")
		}
		return apperr.Invalidf("vault_account_id must name the vault account the gateway logs in to the database with")
	}
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
		return apperr.Invalidf("the gateway logs in with a password account (a password, token, access key or client secret), not an SSH key")
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
	case RDP:
		return "Use the " + name + " desktop", "Remote desktop at " + address + " in the browser, through Grantra. The screen is recorded."
	case AWS:
		return "Open the " + name + " AWS console", "The AWS console and CLI as the role " + address + ", with credentials that end with the session."
	case Azure:
		return "Open the " + name + " Azure portal", "The Azure portal with a role on " + address + " that Grantra takes back when the session ends."
	case GCP:
		return "Open the " + name + " Google Cloud console", "The Google Cloud project " + address + " with a role that expires with the session."
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
	if !slices.Contains(Kinds, in.Kind) {
		return Target{}, apperr.Invalidf("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	if in.Risk == "" {
		in.Risk = "high"
	}
	if in.Risk != "high" && in.Risk != "critical" {
		return Target{}, apperr.Invalidf("risk must be high or critical: access through the gateway is privileged")
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
		                              kube_groups, notes, entitlement_id, max_token_hours, created_by, clipboard, cloud_role, cloud_directory)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) ON CONFLICT DO NOTHING RETURNING id`,
		tenantID, name, in.Kind, c.address, db.Nullable(c.database), c.tlsMode, db.Nullable(c.caCert), db.Nullable(c.vaultAccountID),
		c.kubeGroups, db.Nullable(c.notes), ent.ID, c.maxTokenHours, actor, db.Nullable(c.clipboard), db.Nullable(c.cloudRole),
		db.Nullable(c.cloudDirectory)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.Conflictf("a target named %q already exists", name)
	}
	if err != nil {
		return Target{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "gateway.target_created", "gateway_target", id,
		map[string]any{"name": name, "kind": in.Kind, "address": c.address, "tls_mode": c.tlsMode,
			"vault_account_id": c.vaultAccountID, "kube_groups": c.kubeGroups, "clipboard": c.clipboard, "cloud_role": c.cloudRole,
			"cloud_directory": c.cloudDirectory, "entitlement_id": ent.ID}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// ensureApplication returns the "Remote access" application,
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
		vaultAccountID: cur.VaultAccountID, notes: cur.Notes, kubeGroups: cur.KubeGroups, maxTokenHours: cur.MaxTokenHours,
		clipboard: cur.Clipboard, cloudRole: cur.CloudRole, cloudDirectory: cur.CloudDirectory}
	c.apply(s)
	if err := c.check(ctx, tx, cur.Kind, cur.Name); err != nil {
		return Target{}, err
	}
	_, err = tx.Exec(ctx,
		`UPDATE gateway_targets SET address = $2, database = $3, tls_mode = $4, ca_certificate = $5, vault_account_id = $6,
		        kube_groups = $7, notes = $8, max_token_hours = $9, clipboard = $10, cloud_role = $11, cloud_directory = $12,
		        updated_at = now()
		 WHERE id = $1`, id, c.address, db.Nullable(c.database), c.tlsMode, db.Nullable(c.caCert), db.Nullable(c.vaultAccountID),
		c.kubeGroups, db.Nullable(c.notes), c.maxTokenHours, db.Nullable(c.clipboard), db.Nullable(c.cloudRole),
		db.Nullable(c.cloudDirectory))
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
			"kube_groups": c.kubeGroups, "clipboard": c.clipboard, "cloud_role": c.cloudRole, "cloud_directory": c.cloudDirectory,
			"max_token_hours": c.maxTokenHours}); err != nil {
		return Target{}, err
	}
	return Get(ctx, tx, id)
}

// Delete removes a target and the entitlement that gave access to it, with
// every assignment, request and token of it. Sessions and their logs stay;
// running ones end at the gateway's next check. A cloud console goes only
// once its sessions ended.
func Delete(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	cur, err := lockTarget(ctx, tx, id)
	if err != nil {
		return err
	}
	if Cloud(cur.Kind) {
		// What their sessions granted in the cloud is taken back with the
		// target's settings and account.
		var running int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway_sessions WHERE target_id = $1 AND ended_at IS NULL`, id).
			Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return apperr.Conflictf("%s has %d console sessions running: end them first, so Grantra takes back what it granted in the cloud", cur.Name, running)
		}
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
	e.risk, t.max_token_hours, t.created_at, t.updated_at, coalesce(t.clipboard, ''), coalesce(t.cloud_role, ''),
	coalesce(t.cloud_directory, '')
	FROM gateway_targets t JOIN entitlements e ON e.id = t.entitlement_id
	LEFT JOIN vault_accounts va ON va.id = t.vault_account_id`

func scanTarget(row pgx.CollectableRow) (Target, error) {
	var t Target
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Address, &t.Database, &t.TLSMode, &t.CACertificate, &t.VaultAccountID,
		&t.VaultAccountName, &t.KubeGroups, &t.Notes, &t.EntitlementID, &t.EntitlementName, &t.Risk, &t.MaxTokenHours,
		&t.CreatedAt, &t.UpdatedAt, &t.Clipboard, &t.CloudRole, &t.CloudDirectory)
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
