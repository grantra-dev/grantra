// Package scim connects Grantra to any application that speaks SCIM 2.0
// (RFC 7643, RFC 7644), such as Okta, Entra ID, Slack, GitHub Enterprise and
// most SaaS tools. Groups become entitlements; group membership (and the
// read-only groups attribute of users) becomes the entitlements of accounts.
package scim

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "scim"

func init() { connector.Register(Kind, New) }

// Config is the SCIM connector's settings.
type Config struct {
	// BaseURL is the SCIM endpoint, e.g. https://crm.example.com/scim/v2.
	BaseURL string `json:"base_url"`
	// Token is a reference to the bearer token, e.g. "env:GRANTRA_SECRET_CRM_SCIM_TOKEN".
	Token string `json:"token"`
	// PageSize is how many resources to ask for per request. Default 100.
	PageSize int `json:"page_size,omitempty"`
	// AllowHTTP permits a plain http base URL, for labs and tests only.
	AllowHTTP bool `json:"allow_http,omitempty"`
}

// Client reads one SCIM service.
type Client struct {
	api      *httpapi.Client
	pageSize int
}

// New builds a SCIM connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	u, err := httpapi.BaseURL(Kind, "base_url", cfg.BaseURL, cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	token, err := connector.Secret(Kind, "token", cfg.Token, "env:GRANTRA_SECRET_CRM_SCIM_TOKEN", secrets)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.PageSize == 0:
		cfg.PageSize = 100
	case cfg.PageSize < 1 || cfg.PageSize > 1000:
		return nil, errors.New("scim config: page_size must be between 1 and 1000")
	}
	api := httpapi.New(u, httpapi.Bearer(token))
	api.Accept, api.ContentType = "application/scim+json, application/json", "application/scim+json"
	return &Client{api: api, pageSize: cfg.PageSize}, nil
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
	query := func() string {
		return "/" + resource + "?" + url.Values{"startIndex": {strconv.Itoa(start)}, "count": {strconv.Itoa(c.pageSize)}}.Encode()
	}
	err := httpapi.Pages(ctx, c.api, query(), func(page *struct {
		TotalResults int `json:"totalResults"`
		Resources    []T `json:"Resources"`
	}, _ http.Header) (string, error) {
		for _, r := range page.Resources {
			fn(r)
		}
		seen += len(page.Resources)
		if len(page.Resources) == 0 || seen >= page.TotalResults {
			return "", nil
		}
		if seen > maxResources {
			return "", fmt.Errorf("more than %d resources", maxResources)
		}
		start += len(page.Resources)
		return query(), nil
	})
	if err != nil {
		return fmt.Errorf("list %s: %w", resource, err)
	}
	return nil
}

// Read returns every group and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var b connector.Builder
	err := list(ctx, c, "Groups", func(g group) {
		b.Entitlement(connector.Entitlement{NativeID: g.ID, Name: g.DisplayName})
		for _, m := range g.Members {
			b.Grant(m.Value, g.ID)
		}
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = list(ctx, c, "Users", func(u user) {
		a := connector.Account{
			NativeID:    u.ID,
			Username:    u.UserName,
			ExternalID:  cmp.Or(u.ExternalID, u.Enterprise.EmployeeNumber),
			DisplayName: cmp.Or(u.DisplayName, u.Name.Formatted, strings.TrimSpace(u.Name.GivenName+" "+u.Name.FamilyName)),
			Active:      u.Active == nil || *u.Active,
			Attributes:  connector.Attributes("title", u.Title, "department", u.Enterprise.Department, "user_type", u.UserType),
		}
		for _, e := range u.Emails {
			if a.Email == "" || e.Primary {
				a.Email = e.Value
			}
		}
		for _, g := range u.Groups {
			a.Entitlements = append(a.Entitlements, g.Value)
		}
		b.Account(a)
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return b.Snapshot(), nil
}

const (
	userSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	patchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// send makes a write request to resource, or to one of its items when id is
// not empty, and decodes the answer into v when v is not nil.
func (c *Client) send(ctx context.Context, method, resource, id string, body, v any) error {
	path := "/" + resource
	if id != "" {
		// Native ids can hold any character, slashes included.
		path += "/" + url.PathEscape(id)
	}
	_, err := c.api.Do(ctx, method, path, body, v)
	return err
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
