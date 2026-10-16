// Package ldap connects Grantra to an LDAP directory: Active Directory or
// OpenLDAP. Groups become entitlements and their members the entitlements of
// accounts. It provisions group memberships and enables or disables accounts;
// it does not create accounts, which directories usually get from their own
// onboarding process.
package ldap

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-ldap/ldap/v3"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// Kind is the connector name applications use.
const Kind = "ldap"

func init() {
	connector.Register(Kind, New)
	connector.RegisterPasswordSetter(Kind)
}

// Directory flavors, which set the defaults for filters and attributes.
const (
	ActiveDirectory = "active_directory"
	OpenLDAP        = "openldap"
)

// Config is the LDAP connector's settings.
type Config struct {
	// URL is the server, e.g. ldaps://dc1.example.com.
	URL string `json:"url"`
	// StartTLS upgrades a ldap:// connection to TLS before binding.
	StartTLS bool `json:"start_tls,omitempty"`
	// AllowInsecure permits ldap:// without StartTLS, for labs and tests
	// only: the password would cross the network in the clear.
	AllowInsecure bool `json:"allow_insecure,omitempty"`
	// CACert is a reference to the PEM certificate of the CA that signed the
	// server's certificate, for directories with an internal CA.
	CACert string `json:"ca_cert,omitempty"`
	// BindDN and Password are the service account Grantra signs in with.
	// Password is a reference, e.g. "env:GRANTRA_SECRET_AD_PASSWORD".
	BindDN   string `json:"bind_dn"`
	Password string `json:"password"`
	// BaseDN is where users and groups are searched, unless UserBaseDN or
	// GroupBaseDN narrow it.
	BaseDN      string `json:"base_dn"`
	UserBaseDN  string `json:"user_base_dn,omitempty"`
	GroupBaseDN string `json:"group_base_dn,omitempty"`
	// Directory is active_directory (the default) or openldap.
	Directory string `json:"directory,omitempty"`
	// UserFilter and GroupFilter replace the directory's default filters.
	UserFilter  string `json:"user_filter,omitempty"`
	GroupFilter string `json:"group_filter,omitempty"`
	// Attributes replace the directory's default attribute names.
	Attributes Attributes `json:"attributes,omitzero"`
	// PageSize is how many entries to ask for per page. Default 500.
	PageSize int `json:"page_size,omitempty"`
}

// Attributes name the directory attributes Grantra reads.
type Attributes struct {
	// ID is the stable id of users and groups, which survives renames and
	// moves: objectGUID in Active Directory, entryUUID in OpenLDAP.
	ID          string `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
	// GroupName is the group attribute shown as the entitlement's name.
	GroupName string `json:"group_name,omitempty"`
	// Member is the group attribute that lists member DNs.
	Member string `json:"member,omitempty"`
}

var defaults = map[string]struct {
	users, groups string
	attrs         Attributes
}{
	ActiveDirectory: {
		users:  "(&(objectCategory=person)(objectClass=user))",
		groups: "(objectClass=group)",
		attrs: Attributes{ID: "objectGUID", Username: "sAMAccountName", Email: "mail", DisplayName: "displayName",
			ExternalID: "employeeID", GroupName: "cn", Member: "member"},
	},
	OpenLDAP: {
		users:  "(objectClass=inetOrgPerson)",
		groups: "(|(objectClass=groupOfNames)(objectClass=groupOfUniqueNames))",
		attrs: Attributes{ID: "entryUUID", Username: "uid", Email: "mail", DisplayName: "cn",
			ExternalID: "employeeNumber", GroupName: "cn", Member: "member"},
	},
}

// Client reads and writes one directory.
type Client struct {
	url                     string
	startTLS                bool
	tls                     *tls.Config
	bindDN, password        string
	userBase, groupBase     string
	userFilter, groupFilter string
	directory               string
	attrs                   Attributes
	pageSize                uint32
	timeout                 time.Duration
}

// New builds an LDAP connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("ldap config: %w", err)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || (u.Scheme != "ldap" && u.Scheme != "ldaps") || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("ldap config: url must look like ldaps://dc1.example.com, got %q", cfg.URL)
	}
	switch {
	case u.Scheme == "ldaps" && cfg.StartTLS:
		return nil, errors.New("ldap config: start_tls is for ldap:// URLs; ldaps:// is already TLS")
	case u.Scheme == "ldap" && !cfg.StartTLS && !cfg.AllowInsecure:
		return nil, errors.New("ldap config: use ldaps://, or ldap:// with start_tls (set allow_insecure only for labs)")
	}
	if cfg.Directory == "" {
		cfg.Directory = ActiveDirectory
	}
	def, ok := defaults[cfg.Directory]
	if !ok {
		return nil, fmt.Errorf("ldap config: directory must be %s or %s, got %q", ActiveDirectory, OpenLDAP, cfg.Directory)
	}
	if cfg.BindDN == "" || cfg.Password == "" {
		return nil, errors.New(`ldap config: bind_dn and password are required; password is a reference like "env:GRANTRA_SECRET_AD_PASSWORD"`)
	}
	password, err := secrets(cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("ldap config: password: %w", err)
	}
	if cfg.BaseDN == "" {
		return nil, errors.New("ldap config: base_dn is required, e.g. DC=example,DC=com")
	}
	for name, dn := range map[string]string{"bind_dn": cfg.BindDN, "base_dn": cfg.BaseDN, "user_base_dn": cfg.UserBaseDN, "group_base_dn": cfg.GroupBaseDN} {
		if _, err := ldap.ParseDN(dn); err != nil {
			return nil, fmt.Errorf("ldap config: %s: %w", name, err)
		}
	}
	c := &Client{
		url: cfg.URL, startTLS: cfg.StartTLS, bindDN: cfg.BindDN, password: password,
		userBase: or(cfg.UserBaseDN, cfg.BaseDN), groupBase: or(cfg.GroupBaseDN, cfg.BaseDN),
		userFilter: or(cfg.UserFilter, def.users), groupFilter: or(cfg.GroupFilter, def.groups),
		directory: cfg.Directory, timeout: 60 * time.Second,
		tls: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
	}
	for name, f := range map[string]string{"user_filter": c.userFilter, "group_filter": c.groupFilter} {
		if _, err := ldap.CompileFilter(f); err != nil {
			return nil, fmt.Errorf("ldap config: %s: %w", name, err)
		}
	}
	a, d := cfg.Attributes, def.attrs
	c.attrs = Attributes{ID: or(a.ID, d.ID), Username: or(a.Username, d.Username), Email: or(a.Email, d.Email),
		DisplayName: or(a.DisplayName, d.DisplayName), ExternalID: or(a.ExternalID, d.ExternalID),
		GroupName: or(a.GroupName, d.GroupName), Member: or(a.Member, d.Member)}
	if cfg.CACert != "" {
		pem, err := secrets(cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("ldap config: ca_cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, errors.New("ldap config: ca_cert holds no PEM certificate")
		}
		c.tls.RootCAs = pool
	}
	switch {
	case cfg.PageSize == 0:
		c.pageSize = 500
	case cfg.PageSize < 1 || cfg.PageSize > 1000:
		return nil, errors.New("ldap config: page_size must be between 1 and 1000")
	default:
		c.pageSize = uint32(cfg.PageSize)
	}
	return c, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// session opens a connection, signs in and runs fn. Every Read and every
// change uses its own connection, closed when it is done.
func (c *Client) session(ctx context.Context, fn func(*ldap.Conn) error) error {
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := ldap.DialURL(c.url, ldap.DialWithDialer(d), ldap.DialWithTLSConfig(c.tls))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	timeout := c.timeout
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	conn.SetTimeout(timeout)
	// Close the connection when ctx ends, so a call in flight returns.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err = func() error {
		if c.startTLS {
			if err := conn.StartTLS(c.tls); err != nil {
				return fmt.Errorf("start tls: %w", err)
			}
		}
		if err := conn.Bind(c.bindDN, c.password); err != nil {
			return fmt.Errorf("bind as %s: %w", c.bindDN, err)
		}
		return fn(conn)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// maxEntries stops a misconfigured filter from reading a whole forest.
const maxEntries = 1_000_000

func (c *Client) search(conn *ldap.Conn, base, filter string, attrs []string) ([]*ldap.Entry, error) {
	req := ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, filter, attrs, nil)
	res, err := conn.SearchWithPaging(req, c.pageSize)
	if err != nil {
		return nil, err
	}
	if len(res.Entries) > maxEntries {
		return nil, fmt.Errorf("more than %d entries under %s", maxEntries, base)
	}
	return res.Entries, nil
}

// Read returns every group and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var snap connector.Snapshot
	err := c.session(ctx, func(conn *ldap.Conn) error {
		a := c.attrs
		userAttrs := []string{a.ID, a.Username, a.Email, a.DisplayName, a.ExternalID, "title", "department", "userPrincipalName"}
		switch c.directory {
		case ActiveDirectory:
			userAttrs = append(userAttrs, "userAccountControl")
		case OpenLDAP:
			userAttrs = append(userAttrs, "pwdAccountLockedTime")
		}
		users, err := c.search(conn, c.userBase, c.userFilter, userAttrs)
		if err != nil {
			return fmt.Errorf("search users: %w", err)
		}
		groups, err := c.search(conn, c.groupBase, c.groupFilter, []string{a.ID, a.GroupName, "description", a.Member})
		if err != nil {
			return fmt.Errorf("search groups: %w", err)
		}

		byDN := map[string]int{} // normalized user DN -> index in snap.Accounts
		for _, e := range users {
			id, err := c.id(e)
			if err != nil {
				return err
			}
			acct := connector.Account{
				NativeID:    id,
				Username:    e.GetEqualFoldAttributeValue(a.Username),
				Email:       e.GetEqualFoldAttributeValue(a.Email),
				DisplayName: e.GetEqualFoldAttributeValue(a.DisplayName),
				ExternalID:  e.GetEqualFoldAttributeValue(a.ExternalID),
				Active:      c.active(e),
				Attributes:  map[string]string{"dn": e.DN},
			}
			for k, attr := range map[string]string{"title": "title", "department": "department", "upn": "userPrincipalName"} {
				if v := e.GetEqualFoldAttributeValue(attr); v != "" {
					acct.Attributes[k] = v
				}
			}
			byDN[normDN(e.DN)] = len(snap.Accounts)
			snap.Accounts = append(snap.Accounts, acct)
		}

		for _, g := range groups {
			id, err := c.id(g)
			if err != nil {
				return err
			}
			name := g.GetEqualFoldAttributeValue(a.GroupName)
			if name == "" {
				name = g.DN
			}
			snap.Entitlements = append(snap.Entitlements, connector.Entitlement{
				NativeID: id, Name: name, Description: g.GetEqualFoldAttributeValue("description"),
			})
			members, err := c.members(conn, g)
			if err != nil {
				return fmt.Errorf("members of %s: %w", g.DN, err)
			}
			// Members that are not users read above (nested groups,
			// computers, people outside the user filter) are left out.
			seen := map[int]bool{}
			for _, m := range members {
				if i, ok := byDN[normDN(m)]; ok && !seen[i] {
					seen[i] = true
					snap.Accounts[i].Entitlements = append(snap.Accounts[i].Entitlements, id)
				}
			}
		}
		return nil
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return snap, nil
}

// members returns a group's member DNs. Active Directory hands out large
// groups in ranges ("member;range=0-1499"); the rest is fetched range by
// range.
func (c *Client) members(conn *ldap.Conn, g *ldap.Entry) ([]string, error) {
	attr := c.attrs.Member
	var out []string
	var next int
	ranged := false
	for _, ea := range g.Attributes {
		name, rng, ok := strings.Cut(ea.Name, ";")
		if !strings.EqualFold(name, attr) {
			continue
		}
		out = append(out, ea.Values...)
		if ok {
			end, more, err := rangeEnd(rng)
			if err != nil {
				return nil, err
			}
			ranged, next = more, end+1
		}
	}
	for ranged {
		if len(out) > maxEntries {
			return nil, fmt.Errorf("more than %d members", maxEntries)
		}
		want := fmt.Sprintf("%s;range=%d-*", attr, next)
		req := ldap.NewSearchRequest(g.DN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", []string{want}, nil)
		res, err := conn.Search(req)
		if err != nil {
			return nil, err
		}
		if len(res.Entries) != 1 {
			return nil, errors.New("the group disappeared while its members were read")
		}
		ranged = false
		for _, ea := range res.Entries[0].Attributes {
			name, rng, ok := strings.Cut(ea.Name, ";")
			if !strings.EqualFold(name, attr) || !ok {
				continue
			}
			end, more, err := rangeEnd(rng)
			if err != nil {
				return nil, err
			}
			if more && end+1 <= next {
				return nil, fmt.Errorf("the directory returned range %q after %d", rng, next)
			}
			out = append(out, ea.Values...)
			ranged, next = more, end+1
		}
	}
	return out, nil
}

// rangeEnd reads "range=0-1499" (more to come) or "range=1500-*" (the last).
func rangeEnd(opt string) (end int, more bool, err error) {
	r, ok := strings.CutPrefix(strings.ToLower(opt), "range=")
	_, hi, ok2 := strings.Cut(r, "-")
	if !ok || !ok2 {
		return 0, false, fmt.Errorf("unexpected attribute option %q", opt)
	}
	if hi == "*" {
		return 0, false, nil
	}
	end, err = strconv.Atoi(hi)
	if err != nil {
		return 0, false, fmt.Errorf("unexpected attribute option %q", opt)
	}
	return end, true, nil
}

// adDisabled is the ACCOUNTDISABLE flag of userAccountControl.
const adDisabled = 0x2

// lockedForever is the pwdAccountLockedTime value that locks an OpenLDAP
// account until an administrator unlocks it (ppolicy overlay). Other values
// are temporary lockouts after failed logins, not a disabled account.
const lockedForever = "000001010000Z"

func (c *Client) active(e *ldap.Entry) bool {
	switch c.directory {
	case ActiveDirectory:
		uac, err := strconv.ParseInt(e.GetEqualFoldAttributeValue("userAccountControl"), 10, 64)
		return err != nil || uac&adDisabled == 0
	case OpenLDAP:
		return e.GetEqualFoldAttributeValue("pwdAccountLockedTime") != lockedForever
	}
	return true
}

// guid reports whether the id attribute is Active Directory's binary
// objectGUID, shown in its usual text form.
func (c *Client) guid() bool { return strings.EqualFold(c.attrs.ID, "objectGUID") }

func (c *Client) id(e *ldap.Entry) (string, error) {
	if c.guid() {
		b := e.GetEqualFoldRawAttributeValue(c.attrs.ID)
		if len(b) != 16 {
			return "", fmt.Errorf("%s has no valid objectGUID", e.DN)
		}
		return formatGUID(b), nil
	}
	v := e.GetEqualFoldAttributeValue(c.attrs.ID)
	if v == "" {
		return "", fmt.Errorf("%s has no %s", e.DN, c.attrs.ID)
	}
	return v, nil
}

// formatGUID writes an objectGUID the way Windows does: the first three
// fields are little-endian.
func formatGUID(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]), binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}

func parseGUID(s string) ([]byte, error) {
	h, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(h) != 16 || len(s) != 36 {
		return nil, fmt.Errorf("%q is not a GUID", s)
	}
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:4], binary.BigEndian.Uint32(h[0:4]))
	binary.LittleEndian.PutUint16(b[4:6], binary.BigEndian.Uint16(h[4:6]))
	binary.LittleEndian.PutUint16(b[6:8], binary.BigEndian.Uint16(h[6:8]))
	copy(b[8:], h[8:])
	return b, nil
}

// idFilter matches the entry with the given native id.
func (c *Client) idFilter(id string) (string, error) {
	if !c.guid() {
		return "(" + c.attrs.ID + "=" + ldap.EscapeFilter(id) + ")", nil
	}
	b, err := parseGUID(id)
	if err != nil {
		return "", err
	}
	var f strings.Builder
	f.WriteString("(objectGUID=")
	for _, x := range b {
		fmt.Fprintf(&f, `\%02x`, x)
	}
	f.WriteString(")")
	return f.String(), nil
}

// find returns the one entry under base with the given native id.
func (c *Client) find(conn *ldap.Conn, base, id string, attrs ...string) (*ldap.Entry, error) {
	f, err := c.idFilter(id)
	if err != nil {
		return nil, err
	}
	req := ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 0, false, f, append(attrs, c.attrs.ID), nil)
	res, err := conn.Search(req)
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, fmt.Errorf("find %s: %w", id, err)
	}
	switch {
	case res == nil || len(res.Entries) == 0:
		return nil, fmt.Errorf("%s not found under %s", id, base)
	case len(res.Entries) > 1:
		return nil, fmt.Errorf("%s matches more than one entry", id)
	}
	return res.Entries[0], nil
}

// SetActive enables or disables a user: the ACCOUNTDISABLE flag in Active
// Directory, a permanent pwdAccountLockedTime in OpenLDAP (which needs the
// ppolicy overlay).
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	return c.session(ctx, func(conn *ldap.Conn) error {
		switch c.directory {
		case ActiveDirectory:
			u, err := c.find(conn, c.userBase, accountID, "userAccountControl")
			if err != nil {
				return err
			}
			uac, err := strconv.ParseInt(u.GetEqualFoldAttributeValue("userAccountControl"), 10, 64)
			if err != nil {
				return fmt.Errorf("%s has no valid userAccountControl", u.DN)
			}
			next := uac | adDisabled
			if active {
				next = uac &^ adDisabled
			}
			if next == uac {
				return nil
			}
			m := ldap.NewModifyRequest(u.DN, nil)
			m.Replace("userAccountControl", []string{strconv.FormatInt(next, 10)})
			if err := conn.Modify(m); err != nil {
				return fmt.Errorf("modify %s: %w", u.DN, err)
			}
			return nil
		default:
			u, err := c.find(conn, c.userBase, accountID, "pwdAccountLockedTime")
			if err != nil {
				return err
			}
			locked := u.GetEqualFoldAttributeValue("pwdAccountLockedTime")
			m := ldap.NewModifyRequest(u.DN, nil)
			switch {
			case active && locked == "":
				return nil
			case active:
				m.Delete("pwdAccountLockedTime", nil)
			case locked == lockedForever:
				return nil
			default:
				m.Replace("pwdAccountLockedTime", []string{lockedForever})
			}
			err = conn.Modify(m)
			if err != nil && !(active && ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchAttribute)) {
				return fmt.Errorf("modify %s: %w", u.DN, err)
			}
			return nil
		}
	})
}

// AddMember adds a user to a group.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	return c.membership(ctx, entitlementID, accountID, true)
}

// RemoveMember removes a user from a group.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	return c.membership(ctx, entitlementID, accountID, false)
}

func (c *Client) membership(ctx context.Context, groupID, userID string, add bool) error {
	return c.session(ctx, func(conn *ldap.Conn) error {
		g, err := c.find(conn, c.groupBase, groupID)
		if err != nil {
			return err
		}
		u, err := c.find(conn, c.userBase, userID)
		if err != nil {
			return err
		}
		// Ask the directory whether the user is a member: the group may have
		// more members than one read returns.
		f := "(" + c.attrs.Member + "=" + ldap.EscapeFilter(u.DN) + ")"
		res, err := conn.Search(ldap.NewSearchRequest(g.DN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false, f, []string{"1.1"}, nil))
		if err != nil {
			return fmt.Errorf("check membership of %s: %w", g.DN, err)
		}
		if member := len(res.Entries) == 1; member == add {
			return nil
		}
		m := ldap.NewModifyRequest(g.DN, nil)
		if add {
			m.Add(c.attrs.Member, []string{u.DN})
		} else {
			m.Delete(c.attrs.Member, []string{u.DN})
		}
		err = conn.Modify(m)
		switch {
		case err == nil:
			return nil
		case add && (ldap.IsErrorWithCode(err, ldap.LDAPResultAttributeOrValueExists) || ldap.IsErrorWithCode(err, ldap.LDAPResultEntryAlreadyExists)):
			return nil // someone added it meanwhile
		case !add && ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchAttribute):
			return nil
		case !add && ldap.IsErrorWithCode(err, ldap.LDAPResultObjectClassViolation):
			// groupOfNames must keep at least one member.
			return fmt.Errorf("%s cannot lose its last member; give the group a placeholder member (its owner, say): %w", g.DN, err)
		}
		return fmt.Errorf("modify %s: %w", g.DN, err)
	})
}

// SetPassword sets a user's password: unicodePwd in Active Directory, which
// takes it only over an encrypted connection, and the Password Modify
// extended operation in OpenLDAP, which lets the server hash it.
func (c *Client) SetPassword(ctx context.Context, accountID, password string) error {
	return c.session(ctx, func(conn *ldap.Conn) error {
		u, err := c.find(conn, c.userBase, accountID)
		if err != nil {
			return err
		}
		if c.directory == ActiveDirectory {
			m := ldap.NewModifyRequest(u.DN, nil)
			m.Replace("unicodePwd", []string{adPassword(password)})
			if err := conn.Modify(m); err != nil {
				return fmt.Errorf("set the password of %s: %w", u.DN, err)
			}
			return nil
		}
		if _, err := conn.PasswordModify(ldap.NewPasswordModifyRequest(u.DN, "", password)); err != nil {
			return fmt.Errorf("set the password of %s: %w", u.DN, err)
		}
		return nil
	})
}

// adPassword encodes a password the way Active Directory's unicodePwd wants
// it: in double quotes, as UTF-16 little endian.
func adPassword(password string) string {
	units := utf16.Encode([]rune(`"` + password + `"`))
	b := make([]byte, 0, 2*len(units))
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

// normDN makes DNs comparable: "CN=Ana García, OU=People,DC=example,DC=com"
// and "cn=ana garcía,ou=people,dc=example,dc=com" are the same entry.
func normDN(dn string) string {
	if p, err := ldap.ParseDN(dn); err == nil {
		dn = p.String()
	}
	return strings.ToLower(dn)
}
