// Package googleworkspace connects Grantra to Google Workspace through the
// Admin SDK Directory API. Groups and admin roles become entitlements; users
// become accounts, with their last sign-in.
//
// Grantra signs in as a service account with domain-wide delegation,
// acting as an administrator. Reading asks only for read-only scopes; the
// scopes that change users and groups are asked for the first time
// provisioning changes something, so a read-only setup never needs them.
package googleworkspace

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "google-workspace"

func init() { connector.Register(Kind, New) }

// Scopes the service account must be allowed in domain-wide delegation.
var (
	ReadScopes = []string{
		"https://www.googleapis.com/auth/admin.directory.user.readonly",
		"https://www.googleapis.com/auth/admin.directory.group.readonly",
		"https://www.googleapis.com/auth/admin.directory.rolemanagement.readonly",
	}
	WriteScopes = []string{
		"https://www.googleapis.com/auth/admin.directory.user",
		"https://www.googleapis.com/auth/admin.directory.group.member",
	}
)

// Config is the Google Workspace connector's settings.
type Config struct {
	// AdminEmail is the administrator the service account acts as.
	AdminEmail string `json:"admin_email"`
	// ServiceAccountKey is a reference to the service account's JSON key.
	ServiceAccountKey string `json:"service_account_key"`
	// Customer is the Workspace customer id. Default my_customer, the
	// administrator's own.
	Customer string `json:"customer,omitempty"`
	// APIURL and TokenURL are for tests. Default Google's.
	APIURL    string `json:"api_url,omitempty"`
	TokenURL  string `json:"token_url,omitempty"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

// Client reads and changes one Google Workspace.
type Client struct {
	read, write *httpapi.Client
	customer    string
}

// New builds a Google Workspace connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	if !strings.Contains(cfg.AdminEmail, "@") {
		return nil, errors.New("google-workspace config: admin_email must be the email of a Workspace administrator")
	}
	key, err := connector.Secret(Kind, "service_account_key", cfg.ServiceAccountKey, "env:GRANTRA_SECRET_GOOGLE_KEY", secrets)
	if err != nil {
		return nil, err
	}
	api, err := httpapi.BaseURL(Kind, "api_url", cmp.Or(cfg.APIURL, "https://admin.googleapis.com"), cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if cfg.TokenURL != "" {
		if _, err := httpapi.BaseURL(Kind, "token_url", cfg.TokenURL, cfg.AllowHTTP); err != nil {
			return nil, err
		}
	}
	if cfg.Customer == "" {
		cfg.Customer = "my_customer"
	}
	client := func(scopes []string) (*httpapi.Client, error) {
		jwt, err := google.JWTConfigFromJSON([]byte(key), scopes...)
		if err != nil {
			return nil, errors.New("google-workspace config: service_account_key is not a service account's JSON key")
		}
		jwt.Subject = cfg.AdminEmail
		if cfg.TokenURL != "" {
			jwt.TokenURL = cfg.TokenURL
		}
		base := *api
		base.Path += "/admin/directory/v1"
		c := httpapi.New(&base, nil)
		c.Auth = httpapi.OAuth2(jwt.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, c.HTTP)))
		return c, nil
	}
	c := &Client{customer: cfg.Customer}
	if c.read, err = client(ReadScopes); err != nil {
		return nil, err
	}
	if c.write, err = client(WriteScopes); err != nil {
		return nil, err
	}
	return c, nil
}

// rolePrefix tells admin roles from groups among native ids.
const rolePrefix = "role:"

// list pages through a Directory API collection; key names the list in
// each page ("users", "groups"...).
func list[T any](ctx context.Context, c *httpapi.Client, path string, query url.Values, key string, fn func(T)) error {
	first := path + "?" + query.Encode()
	return httpapi.Pages(ctx, c, first, func(p *map[string]json.RawMessage, _ http.Header) (string, error) {
		var items []T
		if raw, ok := (*p)[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return "", fmt.Errorf("GET %s: decode %s: %w", path, key, err)
			}
		}
		for _, it := range items {
			fn(it)
		}
		var token string
		if raw, ok := (*p)["nextPageToken"]; ok {
			_ = json.Unmarshal(raw, &token)
		}
		if token == "" {
			return "", nil
		}
		query.Set("pageToken", token)
		return path + "?" + query.Encode(), nil
	})
}

type group struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type member struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type role struct {
	RoleID          json.Number `json:"roleId"`
	RoleName        string      `json:"roleName"`
	RoleDescription string      `json:"roleDescription"`
	IsSuperAdmin    bool        `json:"isSuperAdminRole"`
}

type roleAssignment struct {
	RoleID     json.Number `json:"roleId"`
	AssignedTo string      `json:"assignedTo"`
}

type user struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
	Name         struct {
		FullName string `json:"fullName"`
	} `json:"name"`
	Suspended     bool      `json:"suspended"`
	Archived      bool      `json:"archived"`
	LastLoginTime time.Time `json:"lastLoginTime"`
	OrgUnitPath   string    `json:"orgUnitPath"`
	ExternalIDs   []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"externalIds"`
	Organizations []struct {
		Title      string `json:"title"`
		Department string `json:"department"`
		Primary    bool   `json:"primary"`
	} `json:"organizations"`
}

// neverSignedIn is the time Google gives users who never signed in.
var neverSignedIn = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

// Read returns every group, admin role and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var (
		b      connector.Builder
		groups []group
		roles  []role
	)
	customer := url.Values{"customer": {c.customer}, "maxResults": {"200"}}
	if err := list(ctx, c.read, "/groups", customer, "groups", func(g group) { groups = append(groups, g) }); err != nil {
		return connector.Snapshot{}, err
	}
	for _, g := range groups {
		b.Entitlement(connector.Entitlement{NativeID: g.ID, Name: cmp.Or(g.Name, g.Email), Description: g.Description})
		err := list(ctx, c.read, "/groups/"+url.PathEscape(g.ID)+"/members", url.Values{"maxResults": {"200"}}, "members",
			func(m member) {
				if m.Type == "USER" {
					b.Grant(m.ID, g.ID)
				}
			})
		if err != nil {
			return connector.Snapshot{}, err
		}
	}
	rolesPath := "/customer/" + url.PathEscape(c.customer)
	if err := list(ctx, c.read, rolesPath+"/roles", url.Values{"maxResults": {"100"}}, "items", func(r role) { roles = append(roles, r) }); err != nil {
		return connector.Snapshot{}, err
	}
	for _, r := range roles {
		name := strings.TrimPrefix(r.RoleName, "_")
		if r.IsSuperAdmin {
			name = "Super Admin"
		}
		b.Entitlement(connector.Entitlement{NativeID: rolePrefix + r.RoleID.String(), Name: name + " (admin role)", Description: r.RoleDescription})
	}
	err := list(ctx, c.read, rolesPath+"/roleassignments", url.Values{"maxResults": {"200"}}, "items",
		func(a roleAssignment) { b.Grant(a.AssignedTo, rolePrefix+a.RoleID.String()) })
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = list(ctx, c.read, "/users", url.Values{"customer": {c.customer}, "maxResults": {"500"}, "projection": {"full"}}, "users",
		func(u user) {
			a := connector.Account{
				NativeID:    u.ID,
				Username:    u.PrimaryEmail,
				Email:       u.PrimaryEmail,
				DisplayName: u.Name.FullName,
				Active:      !u.Suspended && !u.Archived,
			}
			for _, id := range u.ExternalIDs {
				if id.Type == "organization" && a.ExternalID == "" {
					a.ExternalID = id.Value
				}
			}
			var title, department string
			for _, o := range u.Organizations {
				if title == "" || o.Primary {
					title, department = o.Title, o.Department
				}
			}
			a.Attributes = connector.Attributes("title", title, "department", department, "org_unit", u.OrgUnitPath)
			if u.LastLoginTime.After(neverSignedIn) {
				a.LastLogin = u.LastLoginTime
			}
			b.Account(a)
		})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return b.Snapshot(), nil
}

// errRoles is returned for changes to admin roles, which stay in the Google
// Admin console.
var errRoles = errors.New("Grantra does not change Google Workspace admin roles: change them in the Google Admin console")

// AddMember adds a user to a group.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	if strings.HasPrefix(entitlementID, rolePrefix) {
		return errRoles
	}
	_, err := c.write.Do(ctx, http.MethodPost, "/groups/"+url.PathEscape(entitlementID)+"/members",
		map[string]string{"id": accountID, "role": "MEMBER"}, nil)
	return httpapi.Ignore(err, http.StatusConflict)
}

// RemoveMember takes a user out of a group.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	if strings.HasPrefix(entitlementID, rolePrefix) {
		return errRoles
	}
	_, err := c.write.Do(ctx, http.MethodDelete, "/groups/"+url.PathEscape(entitlementID)+"/members/"+url.PathEscape(accountID), nil, nil)
	return httpapi.Ignore(err, http.StatusNotFound)
}

// SetActive suspends a user, or lifts the suspension.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	_, err := c.write.Do(ctx, http.MethodPatch, "/users/"+url.PathEscape(accountID), map[string]bool{"suspended": !active}, nil)
	return err
}
