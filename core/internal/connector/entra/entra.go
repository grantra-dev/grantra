// Package entra connects Grantra to Microsoft Entra ID (Azure AD) through
// Microsoft Graph. Security and Microsoft 365 groups and the directory roles
// (Global Administrator and the like) become entitlements; users become
// accounts, with their last sign-in when the tenant's licence shows it.
package entra

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

	"golang.org/x/oauth2/clientcredentials"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "entra-id"

func init() { connector.Register(Kind, New) }

// Config is the Entra ID connector's settings.
type Config struct {
	// TenantID is the directory (tenant) id or a verified domain.
	TenantID string `json:"tenant_id"`
	// ClientID is the application (client) id of the app registration
	// Grantra signs in as.
	ClientID string `json:"client_id"`
	// ClientSecret is a reference to the app registration's client secret.
	ClientSecret string `json:"client_secret"`
	// GraphURL and LoginURL are for national clouds (graph.microsoft.us,
	// login.microsoftonline.us and so on). Default the global cloud.
	GraphURL string `json:"graph_url,omitempty"`
	LoginURL string `json:"login_url,omitempty"`
	// AllowHTTP permits plain http addresses, for labs and tests only.
	AllowHTTP bool `json:"allow_http,omitempty"`
}

// Client reads and changes one Entra ID tenant.
type Client struct {
	api   *httpapi.Client
	graph string
}

// New builds an Entra ID connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.TenantID == "" || cfg.ClientID == "" {
		return nil, errors.New("entra-id config: tenant_id and client_id are required")
	}
	if strings.ContainsAny(cfg.TenantID, "/?#") {
		return nil, fmt.Errorf("entra-id config: tenant_id %q is not a tenant id or domain", cfg.TenantID)
	}
	secret, err := connector.Secret(Kind, "client_secret", cfg.ClientSecret, "env:GRANTRA_SECRET_ENTRA", secrets)
	if err != nil {
		return nil, err
	}
	graph, err := httpapi.BaseURL(Kind, "graph_url", cmp.Or(cfg.GraphURL, "https://graph.microsoft.com"), cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	login, err := httpapi.BaseURL(Kind, "login_url", cmp.Or(cfg.LoginURL, "https://login.microsoftonline.com"), cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	cc := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secret,
		TokenURL:     login.String() + "/" + url.PathEscape(cfg.TenantID) + "/oauth2/v2.0/token",
		Scopes:       []string{graph.String() + "/.default"},
	}
	return &Client{api: httpapi.NewOAuth2(httpapi.At(graph, "/v1.0"), &cc), graph: graph.String()}, nil
}

// rolePrefix tells directory roles from groups among native ids.
const rolePrefix = "role:"

// page is one page of a Graph list.
type page[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

// list pages through a Graph collection.
func list[T any](ctx context.Context, c *Client, path string, fn func(T)) error {
	return httpapi.Pages(ctx, c.api, path, func(p *page[T], _ http.Header) (string, error) {
		for _, v := range p.Value {
			fn(v)
		}
		return p.NextLink, nil
	})
}

// directoryObject is a group or a directory role; members only need ids.
type directoryObject struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type user struct {
	ID                string `json:"id"`
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
	DisplayName       string `json:"displayName"`
	EmployeeID        string `json:"employeeId"`
	AccountEnabled    *bool  `json:"accountEnabled"`
	JobTitle          string `json:"jobTitle"`
	Department        string `json:"department"`
	UserType          string `json:"userType"`
	SignInActivity    *struct {
		LastSignInDateTime time.Time `json:"lastSignInDateTime"`
	} `json:"signInActivity"`
}

const userFields = "id,userPrincipalName,mail,displayName,employeeId,accountEnabled,jobTitle,department,userType"

// Read returns every group, directory role and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var (
		b      connector.Builder
		groups []directoryObject
		roles  []directoryObject
	)
	err := list(ctx, c, "/groups?$select=id,displayName,description&$top=999",
		func(g directoryObject) { groups = append(groups, g) })
	if err != nil {
		return connector.Snapshot{}, err
	}
	for _, g := range groups {
		b.Entitlement(connector.Entitlement{NativeID: g.ID, Name: g.DisplayName, Description: g.Description})
		err := list(ctx, c, "/groups/"+url.PathEscape(g.ID)+"/members?$select=id&$top=999",
			func(m directoryObject) { b.Grant(m.ID, g.ID) })
		if err != nil {
			return connector.Snapshot{}, err
		}
	}
	// Only roles someone holds are activated, and only those are listed.
	if err := list(ctx, c, "/directoryRoles", func(r directoryObject) { roles = append(roles, r) }); err != nil {
		return connector.Snapshot{}, err
	}
	for _, r := range roles {
		id := rolePrefix + r.ID
		b.Entitlement(connector.Entitlement{NativeID: id, Name: r.DisplayName + " (directory role)", Description: r.Description})
		err := list(ctx, c, "/directoryRoles/"+url.PathEscape(r.ID)+"/members?$select=id",
			func(m directoryObject) { b.Grant(m.ID, id) })
		if err != nil {
			return connector.Snapshot{}, err
		}
	}
	// Last sign-ins need an Entra ID P1 licence and AuditLog.Read.All; a
	// tenant without them still gets its users.
	var users []user
	err = list(ctx, c, "/users?$select="+userFields+",signInActivity&$top=999", func(u user) { users = append(users, u) })
	if httpapi.HasStatus(err, http.StatusForbidden) {
		users = nil
		err = list(ctx, c, "/users?$select="+userFields+"&$top=999", func(u user) { users = append(users, u) })
	}
	if err != nil {
		return connector.Snapshot{}, err
	}
	for _, u := range users {
		a := connector.Account{
			NativeID:    u.ID,
			Username:    u.UserPrincipalName,
			Email:       cmp.Or(u.Mail, u.UserPrincipalName),
			DisplayName: u.DisplayName,
			ExternalID:  u.EmployeeID,
			Active:      u.AccountEnabled == nil || *u.AccountEnabled,
			Attributes:  connector.Attributes("title", u.JobTitle, "department", u.Department, "user_type", u.UserType),
		}
		if u.SignInActivity != nil {
			a.LastLogin = u.SignInActivity.LastSignInDateTime
		}
		b.Account(a)
	}
	return b.Snapshot(), nil
}

// objectRef is how Graph names a user in a membership change.
func (c *Client) objectRef(accountID string) map[string]string {
	return map[string]string{"@odata.id": c.graph + "/v1.0/directoryObjects/" + url.PathEscape(accountID)}
}

// membersPath is the members of a group or directory role.
func membersPath(entitlementID string) string {
	if id, ok := strings.CutPrefix(entitlementID, rolePrefix); ok {
		return "/directoryRoles/" + url.PathEscape(id) + "/members"
	}
	return "/groups/" + url.PathEscape(entitlementID) + "/members"
}

// AddMember adds a user to a group or gives them a directory role.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	_, err := c.api.Do(ctx, http.MethodPost, membersPath(entitlementID)+"/$ref", c.objectRef(accountID), nil)
	var e *httpapi.StatusError
	// Graph answers 400 when the user is a member already.
	if errors.As(err, &e) && e.Status == http.StatusBadRequest && strings.Contains(e.Detail, "already exist") {
		return nil
	}
	return err
}

// RemoveMember takes a user out of a group or directory role.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	_, err := c.api.Do(ctx, http.MethodDelete, membersPath(entitlementID)+"/"+url.PathEscape(accountID)+"/$ref", nil, nil)
	return httpapi.Ignore(err, http.StatusNotFound)
}

// SetActive lets a user sign in, or blocks them.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	_, err := c.api.Do(ctx, http.MethodPatch, "/users/"+url.PathEscape(accountID), map[string]bool{"accountEnabled": active}, nil)
	return err
}
