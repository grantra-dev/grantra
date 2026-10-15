// Package grantralogin connects Grantra to Grantra Login, the Keycloak that
// ships with Grantra, through the Keycloak Admin REST API. Realm users become
// accounts; groups and realm roles become entitlements. It provisions too:
// new users get an email to set their own password, so Grantra never chooses
// or sees one.
//
// The connector signs in as a confidential client with a service account
// (client credentials grant) that needs the realm-management roles
// manage-users and view-realm, nothing more.
package grantralogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// Kind is the connector name applications use.
const Kind = "grantra-login"

func init() { connector.Register(Kind, New) }

// Config is the Grantra Login connector's settings.
type Config struct {
	// URL is where Grantra reaches Keycloak, e.g. https://login.example.com.
	URL string `json:"url"`
	// Realm defaults to "grantra".
	Realm string `json:"realm,omitempty"`
	// ClientID is the service-account client, default "grantra-connector".
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is a reference like "env:GRANTRA_SECRET_GRANTRA_LOGIN".
	ClientSecret string `json:"client_secret"`
	// ExternalIDAttribute is the user attribute holding the HR employee id.
	// Default "employee_id".
	ExternalIDAttribute string `json:"external_id_attribute,omitempty"`
	// SetupEmail sends new users the "set your password" email. Default true;
	// turn it off when people sign in through a federated IdP only.
	SetupEmail *bool `json:"setup_email,omitempty"`
	// Roles also reads realm roles as entitlements. Default true.
	Roles *bool `json:"roles,omitempty"`
	// PageSize is how many items to ask for per request. Default 100.
	PageSize int `json:"page_size,omitempty"`
	// AllowHTTP permits a plain http URL, for labs and docker-compose only.
	AllowHTTP bool `json:"allow_http,omitempty"`
}

// Native ids tell groups from realm roles.
const (
	groupPrefix = "group:"
	rolePrefix  = "role:"
)

// builtinRoles are the realm roles Keycloak gives everyone; governing them
// would only add noise.
var builtinRoles = map[string]bool{"offline_access": true, "uma_authorization": true}

// Client talks to one Grantra Login realm.
type Client struct {
	base         *url.URL
	realm        string
	clientID     string
	secret       string
	externalAttr string
	setupEmail   bool
	roles        bool
	pageSize     int
	http         *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New builds a Grantra Login connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("grantra-login config: %w", err)
	}
	u, err := url.Parse(strings.TrimSuffix(cfg.URL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" {
		return nil, fmt.Errorf("grantra-login config: url must be an absolute URL, got %q", cfg.URL)
	}
	if u.Scheme == "http" && !cfg.AllowHTTP {
		return nil, errors.New("grantra-login config: url must use https (set allow_http only for labs)")
	}
	if cfg.Realm == "" {
		cfg.Realm = "grantra"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "grantra-connector"
	}
	if cfg.ExternalIDAttribute == "" {
		cfg.ExternalIDAttribute = "employee_id"
	}
	if cfg.ClientSecret == "" {
		return nil, errors.New(`grantra-login config: client_secret is required, as a reference like "env:GRANTRA_SECRET_GRANTRA_LOGIN"`)
	}
	secret, err := secrets(cfg.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("grantra-login config: client_secret: %w", err)
	}
	switch {
	case cfg.PageSize == 0:
		cfg.PageSize = 100
	case cfg.PageSize < 1 || cfg.PageSize > 1000:
		return nil, errors.New("grantra-login config: page_size must be between 1 and 1000")
	}
	return &Client{
		base: u, realm: cfg.Realm, clientID: cfg.ClientID, secret: secret,
		externalAttr: cfg.ExternalIDAttribute,
		setupEmail:   cfg.SetupEmail == nil || *cfg.SetupEmail,
		roles:        cfg.Roles == nil || *cfg.Roles,
		pageSize:     cfg.PageSize,
		http:         &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// accessToken returns a service-account token, reusing it until shortly
// before it expires.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base.String()+"/realms/"+url.PathEscape(c.realm)+"/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(url.QueryEscape(c.clientID), url.QueryEscape(c.secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("sign in as %s: %w", c.clientID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &t)
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		return "", &apiError{where: "sign in as " + c.clientID, status: resp.StatusCode, msg: t.Error}
	}
	c.token = t.AccessToken
	// Renew a little early so a token never expires mid-request.
	c.expires = time.Now().Add(time.Duration(max(t.ExpiresIn-30, 10)) * time.Second)
	return c.token, nil
}

// apiError is an error answer from the Admin API.
type apiError struct {
	where  string
	status int
	msg    string
}

func (e *apiError) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("%s: %d %s: %s", e.where, e.status, http.StatusText(e.status), e.msg)
	}
	return fmt.Sprintf("%s: %d %s", e.where, e.status, http.StatusText(e.status))
}

// IsRemote reports whether err came from talking to Keycloak: an error
// answer, or a network failure.
func IsRemote(err error) bool {
	var (
		api *apiError
		net *url.Error
	)
	return errors.As(err, &api) || errors.As(err, &net)
}

func hasStatus(err error, status int) bool {
	var e *apiError
	return errors.As(err, &e) && e.status == status
}

// do calls the Admin API. path is relative to the realm and already escaped.
// It returns the response headers so callers can read Location.
func (c *Client) do(ctx context.Context, method, path string, body, v any) (http.Header, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method,
		c.base.String()+"/admin/realms/"+url.PathEscape(c.realm)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	where := method + " " + strings.SplitN(path, "?", 2)[0]
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error        string `json:"error"`
			ErrorMessage string `json:"errorMessage"`
		}
		_ = json.Unmarshal(data, &e)
		return nil, &apiError{where: where, status: resp.StatusCode, msg: e.ErrorMessage + e.Error}
	}
	if v != nil && len(data) > 0 {
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("%s: decode response: %w", where, err)
		}
	}
	return resp.Header, nil
}

// maxItems stops a misbehaving server from paging forever.
const maxItems = 1_000_000

// list pages through a collection with Keycloak's first/max parameters.
func list[T any](ctx context.Context, c *Client, path string, query url.Values, fn func(T)) error {
	for first := 0; ; first += c.pageSize {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("first", strconv.Itoa(first))
		q.Set("max", strconv.Itoa(c.pageSize))
		var page []T
		if _, err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
			return err
		}
		for _, item := range page {
			fn(item)
		}
		if len(page) < c.pageSize {
			return nil
		}
		if first > maxItems {
			return fmt.Errorf("GET %s: more than %d items", path, maxItems)
		}
	}
}

type user struct {
	ID         string              `json:"id"`
	Username   string              `json:"username"`
	Email      string              `json:"email"`
	FirstName  string              `json:"firstName"`
	LastName   string              `json:"lastName"`
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
}

type group struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Path          string              `json:"path"`
	Description   string              `json:"description"`
	Attributes    map[string][]string `json:"attributes"`
	SubGroupCount int                 `json:"subGroupCount"`
}

type role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Read returns every user, group and realm role.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var snap connector.Snapshot
	held := map[string][]string{} // user id -> entitlement native ids
	members := func(entitlement, path string) error {
		return list(ctx, c, path, url.Values{"briefRepresentation": {"true"}}, func(u user) {
			held[u.ID] = append(held[u.ID], entitlement)
		})
	}

	// Groups nest; walk the tree breadth first.
	var groups []group
	if err := list(ctx, c, "/groups", url.Values{"briefRepresentation": {"false"}}, func(g group) { groups = append(groups, g) }); err != nil {
		return connector.Snapshot{}, err
	}
	for i := 0; i < len(groups); i++ {
		g := groups[i]
		if g.SubGroupCount > 0 {
			if err := list(ctx, c, "/groups/"+url.PathEscape(g.ID)+"/children", url.Values{"briefRepresentation": {"false"}},
				func(child group) { groups = append(groups, child) }); err != nil {
				return connector.Snapshot{}, err
			}
		}
		name := strings.TrimPrefix(g.Path, "/")
		if name == "" {
			name = g.Name
		}
		// Keycloak 26.4 has no group description yet; a "description"
		// attribute stands in for it.
		desc := g.Description
		if d := g.Attributes["description"]; desc == "" && len(d) > 0 {
			desc = d[0]
		}
		id := groupPrefix + g.ID
		snap.Entitlements = append(snap.Entitlements, connector.Entitlement{NativeID: id, Name: name, Description: desc})
		if err := members(id, "/groups/"+url.PathEscape(g.ID)+"/members"); err != nil {
			return connector.Snapshot{}, err
		}
	}

	if c.roles {
		var roles []role
		if err := list(ctx, c, "/roles", url.Values{"briefRepresentation": {"false"}}, func(r role) { roles = append(roles, r) }); err != nil {
			return connector.Snapshot{}, err
		}
		for _, r := range roles {
			// The MFA role follows sensitive access on its own (see SyncMFA);
			// governing it as access too would have two owners fight over it.
			if builtinRoles[r.Name] || r.Name == "default-roles-"+c.realm || r.Name == MFARole {
				continue
			}
			id := rolePrefix + r.Name
			snap.Entitlements = append(snap.Entitlements, connector.Entitlement{NativeID: id, Name: r.Name, Description: r.Description})
			// Only direct holders: composite and group-inherited roles are
			// governed through the group or role that carries them.
			if err := members(id, "/roles/"+url.PathEscape(r.Name)+"/users"); err != nil {
				return connector.Snapshot{}, err
			}
		}
	}

	err := list(ctx, c, "/users", url.Values{"briefRepresentation": {"false"}}, func(u user) {
		a := connector.Account{
			NativeID:     u.ID,
			Username:     u.Username,
			Email:        u.Email,
			DisplayName:  strings.TrimSpace(u.FirstName + " " + u.LastName),
			Active:       u.Enabled,
			Entitlements: held[u.ID],
			Attributes:   map[string]string{},
		}
		if v := u.Attributes[c.externalAttr]; len(v) > 0 {
			a.ExternalID = v[0]
		}
		for _, k := range []string{"department", "title"} {
			if v := u.Attributes[k]; len(v) > 0 && v[0] != "" {
				a.Attributes[k] = v[0]
			}
		}
		snap.Accounts = append(snap.Accounts, a)
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return snap, nil
}

// CreateAccount creates an enabled user and, unless setup_email is off, sends
// the "set your password" email. It is safe to repeat: when the username is
// taken, it returns the existing user, so a run that failed after creating
// the user (on the email, say) can simply be retried.
func (c *Client) CreateAccount(ctx context.Context, a connector.NewAccount) (string, error) {
	first, last, _ := strings.Cut(strings.TrimSpace(a.DisplayName), " ")
	body := map[string]any{
		"username":  a.Username,
		"enabled":   true,
		"firstName": first,
		"lastName":  strings.TrimSpace(last),
	}
	if a.Email != "" {
		body["email"] = a.Email
		// The email comes from HR, which Grantra trusts.
		body["emailVerified"] = true
	}
	if a.ExternalID != "" {
		body["attributes"] = map[string][]string{c.externalAttr: {a.ExternalID}}
	}
	if c.setupEmail {
		body["requiredActions"] = []string{"UPDATE_PASSWORD"}
	}
	var id string
	hdr, err := c.do(ctx, http.MethodPost, "/users", body, nil)
	switch {
	case err == nil:
		loc := hdr.Get("Location")
		id = loc[strings.LastIndex(loc, "/")+1:]
		if id == "" {
			return "", errors.New("POST /users: the response has no Location")
		}
	case hasStatus(err, http.StatusConflict):
		if id, err = c.findUser(ctx, a.Username); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	if c.setupEmail && a.Email != "" {
		q := url.Values{"client_id": {"account-console"}, "lifespan": {strconv.Itoa(int((72 * time.Hour).Seconds()))}}
		if _, err := c.do(ctx, http.MethodPut, "/users/"+url.PathEscape(id)+"/execute-actions-email?"+q.Encode(),
			[]string{"UPDATE_PASSWORD"}, nil); err != nil {
			return "", fmt.Errorf("user %s created, but the set-your-password email failed (check the realm's email settings): %w", a.Username, err)
		}
	}
	return id, nil
}

// findUser returns the id of the user with exactly this username.
func (c *Client) findUser(ctx context.Context, username string) (string, error) {
	var found []user
	q := url.Values{"username": {username}, "exact": {"true"}}
	if _, err := c.do(ctx, http.MethodGet, "/users?"+q.Encode(), nil, &found); err != nil {
		return "", err
	}
	for _, u := range found {
		if strings.EqualFold(u.Username, username) {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("POST /users: %s is taken by an email or a user Grantra cannot find", username)
}

// SetActive enables or disables a user. Disabling also ends their sessions.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	path := "/users/" + url.PathEscape(accountID)
	if _, err := c.do(ctx, http.MethodPut, path, map[string]any{"enabled": active}, nil); err != nil {
		return err
	}
	if !active {
		_, err := c.do(ctx, http.MethodPost, path+"/logout", nil, nil)
		return err
	}
	return nil
}

// AddMember adds a user to a group or gives them a realm role.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	return c.membership(ctx, entitlementID, accountID, true)
}

// RemoveMember takes a user out of a group or a realm role.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	return c.membership(ctx, entitlementID, accountID, false)
}

func (c *Client) membership(ctx context.Context, entitlementID, accountID string, add bool) error {
	user := "/users/" + url.PathEscape(accountID)
	if id, ok := strings.CutPrefix(entitlementID, groupPrefix); ok {
		method := http.MethodPut
		if !add {
			method = http.MethodDelete
		}
		_, err := c.do(ctx, method, user+"/groups/"+url.PathEscape(id), nil, nil)
		return err
	}
	if name, ok := strings.CutPrefix(entitlementID, rolePrefix); ok {
		var r role
		if _, err := c.do(ctx, http.MethodGet, "/roles/"+url.PathEscape(name), nil, &r); err != nil {
			return err
		}
		method := http.MethodPost
		if !add {
			method = http.MethodDelete
		}
		_, err := c.do(ctx, method, user+"/role-mappings/realm", []role{{ID: r.ID, Name: r.Name}}, nil)
		return err
	}
	return fmt.Errorf("entitlement %q is neither a Grantra Login group nor a realm role", entitlementID)
}
