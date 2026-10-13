// Package scim connects Grantra to any application that speaks SCIM 2.0
// (RFC 7643, RFC 7644), such as Okta, Entra ID, Slack, GitHub Enterprise and
// most SaaS tools. Groups become entitlements; group membership (and the
// read-only groups attribute of users) becomes the entitlements of accounts.
package scim

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
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// Kind is the connector name applications use.
const Kind = "scim"

func init() { connector.Register(Kind, New) }

// Config is the SCIM connector's settings.
type Config struct {
	// BaseURL is the SCIM endpoint, e.g. https://crm.example.com/scim/v2.
	BaseURL string `json:"base_url"`
	// Token is a reference to the bearer token, e.g. "env:CRM_SCIM_TOKEN".
	Token string `json:"token"`
	// PageSize is how many resources to ask for per request. Default 100.
	PageSize int `json:"page_size,omitempty"`
	// AllowHTTP permits a plain http base URL, for labs and tests only.
	AllowHTTP bool `json:"allow_http,omitempty"`
}

// Client reads one SCIM service.
type Client struct {
	base     *url.URL
	token    string
	pageSize int
	http     *http.Client
}

// New builds a SCIM connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("scim config: %w", err)
	}
	u, err := url.Parse(strings.TrimSuffix(cfg.BaseURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("scim config: base_url must be an absolute URL, got %q", cfg.BaseURL)
	}
	if u.Scheme == "http" && !cfg.AllowHTTP {
		return nil, errors.New("scim config: base_url must use https (set allow_http only for labs)")
	}
	if cfg.Token == "" {
		return nil, errors.New(`scim config: token is required, as a reference like "env:CRM_SCIM_TOKEN"`)
	}
	token, err := secrets(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("scim config: token: %w", err)
	}
	switch {
	case cfg.PageSize == 0:
		cfg.PageSize = 100
	case cfg.PageSize < 1 || cfg.PageSize > 1000:
		return nil, errors.New("scim config: page_size must be between 1 and 1000")
	}
	return &Client{base: u, token: token, pageSize: cfg.PageSize, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

type ref struct {
	Value string `json:"value"`
}

type group struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Members     []ref  `json:"members"`
}

type user struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName"`
	Name        struct {
		Formatted  string `json:"formatted"`
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	// Active is a pointer because a missing value means active.
	Active     *bool  `json:"active"`
	Title      string `json:"title"`
	UserType   string `json:"userType"`
	Groups     []ref  `json:"groups"`
	Enterprise struct {
		Department     string `json:"department"`
		EmployeeNumber string `json:"employeeNumber"`
	} `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`
}

// maxResources stops a misbehaving server from paging forever.
const maxResources = 1_000_000

// list pages through a resource type, decoding each page's Resources into fn.
func list[T any](ctx context.Context, c *Client, resource string, fn func(T)) error {
	start, seen := 1, 0
	for {
		u := *c.base
		u.Path += "/" + resource
		u.RawQuery = url.Values{"startIndex": {strconv.Itoa(start)}, "count": {strconv.Itoa(c.pageSize)}}.Encode()
		var page struct {
			TotalResults int `json:"totalResults"`
			Resources    []T `json:"Resources"`
		}
		if err := c.get(ctx, u.String(), &page); err != nil {
			return fmt.Errorf("list %s: %w", resource, err)
		}
		for _, r := range page.Resources {
			fn(r)
		}
		seen += len(page.Resources)
		if len(page.Resources) == 0 || seen >= page.TotalResults {
			return nil
		}
		if seen > maxResources {
			return fmt.Errorf("list %s: more than %d resources", resource, maxResources)
		}
		start += len(page.Resources)
	}
}

func (c *Client) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/scim+json, application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Detail != "" {
			return fmt.Errorf("%s: %s", resp.Status, e.Detail)
		}
		return errors.New(resp.Status)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Read returns every group and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var snap connector.Snapshot
	members := map[string][]string{} // user id -> group ids
	err := list(ctx, c, "Groups", func(g group) {
		name := g.DisplayName
		if name == "" {
			name = g.ID
		}
		snap.Entitlements = append(snap.Entitlements, connector.Entitlement{NativeID: g.ID, Name: name})
		for _, m := range g.Members {
			members[m.Value] = append(members[m.Value], g.ID)
		}
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = list(ctx, c, "Users", func(u user) {
		a := connector.Account{
			NativeID:    u.ID,
			Username:    u.UserName,
			ExternalID:  u.ExternalID,
			DisplayName: u.DisplayName,
			Active:      u.Active == nil || *u.Active,
			Attributes:  map[string]string{},
		}
		if a.ExternalID == "" {
			a.ExternalID = u.Enterprise.EmployeeNumber
		}
		if a.DisplayName == "" {
			a.DisplayName = u.Name.Formatted
		}
		if a.DisplayName == "" {
			a.DisplayName = strings.TrimSpace(u.Name.GivenName + " " + u.Name.FamilyName)
		}
		for _, e := range u.Emails {
			if a.Email == "" || e.Primary {
				a.Email = e.Value
			}
		}
		for k, v := range map[string]string{"title": u.Title, "department": u.Enterprise.Department, "user_type": u.UserType} {
			if v != "" {
				a.Attributes[k] = v
			}
		}
		groups := map[string]bool{}
		for _, g := range append(members[u.ID], refs(u.Groups)...) {
			if !groups[g] {
				groups[g] = true
				a.Entitlements = append(a.Entitlements, g)
			}
		}
		snap.Accounts = append(snap.Accounts, a)
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return snap, nil
}

func refs(rs []ref) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Value)
	}
	return out
}

const (
	userSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	patchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// send makes a write request to resource, or to one of its items when id is
// not empty, and decodes the answer into v when v is not nil.
func (c *Client) send(ctx context.Context, method, resource, id string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := *c.base
	path := "/" + resource
	u.RawPath = u.EscapedPath() + path
	u.Path += path
	if id != "" {
		// Native ids can hold any character, slashes included.
		path += "/" + id
		u.RawPath += "/" + url.PathEscape(id)
		u.Path += "/" + id
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/scim+json, application/json")
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Detail != "" {
			return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, e.Detail)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if v != nil {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// CreateAccount creates a user.
func (c *Client) CreateAccount(ctx context.Context, a connector.NewAccount) (string, error) {
	body := map[string]any{
		"schemas":  []string{userSchema},
		"userName": a.Username,
		"active":   true,
	}
	if a.DisplayName != "" {
		body["displayName"] = a.DisplayName
		body["name"] = map[string]string{"formatted": a.DisplayName}
	}
	if a.Email != "" {
		body["emails"] = []map[string]any{{"value": a.Email, "type": "work", "primary": true}}
	}
	if a.ExternalID != "" {
		body["externalId"] = a.ExternalID
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.send(ctx, http.MethodPost, "Users", "", body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("POST /Users: the response has no id")
	}
	return created.ID, nil
}

// SetActive enables or disables a user.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	return c.send(ctx, http.MethodPatch, "Users", accountID, map[string]any{
		"schemas":    []string{patchSchema},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": active}},
	}, nil)
}

// AddMember adds a user to a group.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	return c.send(ctx, http.MethodPatch, "Groups", entitlementID, map[string]any{
		"schemas":    []string{patchSchema},
		"Operations": []map[string]any{{"op": "add", "path": "members", "value": []map[string]string{{"value": accountID}}}},
	}, nil)
}

// RemoveMember removes a user from a group.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	return c.send(ctx, http.MethodPatch, "Groups", entitlementID, map[string]any{
		"schemas": []string{patchSchema},
		"Operations": []map[string]any{{"op": "remove",
			"path": fmt.Sprintf(`members[value eq %q]`, accountID)}},
	}, nil)
}
