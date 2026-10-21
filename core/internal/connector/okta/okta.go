// Package okta connects Grantra to Okta through its management API. Groups,
// application assignments and admin roles become entitlements; users become
// accounts, with their last sign-in.
package okta

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

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "okta"

func init() { connector.Register(Kind, New) }

// Config is the Okta connector's settings.
type Config struct {
	// OrgURL is the Okta organization, e.g. https://acme.okta.com.
	OrgURL string `json:"org_url"`
	// Token is a reference to an API token of a read-only administrator (or
	// a group administrator, to also change group members).
	Token string `json:"token"`
	// Apps also reads who is assigned to each active application. Default
	// true.
	Apps *bool `json:"apps,omitempty"`
	// AllowHTTP permits a plain http address, for labs and tests only.
	AllowHTTP bool `json:"allow_http,omitempty"`
}

// Client reads and changes one Okta organization.
type Client struct {
	api  *httpapi.Client
	apps bool
}

// New builds an Okta connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	org, err := httpapi.BaseURL(Kind, "org_url", cfg.OrgURL, cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	token, err := connector.Secret(Kind, "token", cfg.Token, "env:GRANTRA_SECRET_OKTA_TOKEN", secrets)
	if err != nil {
		return nil, err
	}
	org.Path += "/api/v1"
	return &Client{api: httpapi.New(org, httpapi.Header("Authorization", "SSWS "+token)), apps: cfg.Apps == nil || *cfg.Apps}, nil
}

// Native id prefixes of what is not a group.
const (
	appPrefix  = "app:"
	rolePrefix = "role:"
)

type group struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Profile struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"profile"`
}

type app struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type ref struct {
	ID string `json:"id"`
}

type role struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

type user struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	LastLogin time.Time `json:"lastLogin"`
	Profile   struct {
		Login          string `json:"login"`
		Email          string `json:"email"`
		FirstName      string `json:"firstName"`
		LastName       string `json:"lastName"`
		DisplayName    string `json:"displayName"`
		EmployeeNumber string `json:"employeeNumber"`
		Title          string `json:"title"`
		Department     string `json:"department"`
		UserType       string `json:"userType"`
	} `json:"profile"`
}

// active are the statuses of users who can sign in, or are about to.
var active = map[string]bool{"STAGED": true, "PROVISIONED": true, "ACTIVE": true, "RECOVERY": true, "PASSWORD_EXPIRED": true, "LOCKED_OUT": true}

// Read returns every group, active application, admin role and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var (
		b      connector.Builder
		groups []group
		apps   []app
	)
	if err := httpapi.LinkPages(ctx, c.api, "/groups?limit=200", func(g group) { groups = append(groups, g) }); err != nil {
		return connector.Snapshot{}, err
	}
	for _, g := range groups {
		b.Entitlement(connector.Entitlement{NativeID: g.ID, Name: g.Profile.Name, Description: g.Profile.Description})
		if err := httpapi.LinkPages(ctx, c.api, "/groups/"+url.PathEscape(g.ID)+"/users?limit=200", func(u ref) { b.Grant(u.ID, g.ID) }); err != nil {
			return connector.Snapshot{}, err
		}
	}
	if c.apps {
		if err := httpapi.LinkPages(ctx, c.api, `/apps?limit=200&filter=status+eq+"ACTIVE"`, func(a app) { apps = append(apps, a) }); err != nil {
			return connector.Snapshot{}, err
		}
		for _, a := range apps {
			id := appPrefix + a.ID
			b.Entitlement(connector.Entitlement{NativeID: id, Name: a.Label + " (application)"})
			if err := httpapi.LinkPages(ctx, c.api, "/apps/"+url.PathEscape(a.ID)+"/users?limit=500", func(u ref) { b.Grant(u.ID, id) }); err != nil {
				return connector.Snapshot{}, err
			}
		}
	}
	if err := c.readRoles(ctx, &b); err != nil {
		return connector.Snapshot{}, err
	}
	add := func(u user) {
		p := u.Profile
		b.Account(connector.Account{
			NativeID:    u.ID,
			Username:    p.Login,
			Email:       p.Email,
			DisplayName: cmp.Or(p.DisplayName, strings.TrimSpace(p.FirstName+" "+p.LastName)),
			ExternalID:  p.EmployeeNumber,
			Active:      active[u.Status],
			Attributes:  connector.Attributes("title", p.Title, "department", p.Department, "user_type", p.UserType, "status", strings.ToLower(u.Status)),
			LastLogin:   u.LastLogin,
		})
	}
	// The plain list leaves out deactivated users; they are read apart so
	// their accounts show as disabled rather than gone.
	if err := httpapi.LinkPages(ctx, c.api, "/users?limit=200", add); err != nil {
		return connector.Snapshot{}, err
	}
	if err := httpapi.LinkPages(ctx, c.api, `/users?limit=200&filter=status+eq+"DEPROVISIONED"`, add); err != nil {
		return connector.Snapshot{}, err
	}
	return b.Snapshot(), nil
}

// readRoles reads the admin roles of every user who holds one, directly or
// through a group.
func (c *Client) readRoles(ctx context.Context, b *connector.Builder) error {
	var admins []ref
	err := httpapi.Pages(ctx, c.api, "/iam/assignees/users?limit=100", func(p *struct {
		Value []ref `json:"value"`
		Links struct {
			Next struct {
				Href string `json:"href"`
			} `json:"next"`
		} `json:"_links"`
	}, _ http.Header) (string, error) {
		admins = append(admins, p.Value...)
		return p.Links.Next.Href, nil
	})
	if err != nil {
		return err
	}
	for _, u := range admins {
		var roles []role
		if _, err := c.api.Get(ctx, "/users/"+url.PathEscape(u.ID)+"/roles", &roles); err != nil {
			return err
		}
		for _, r := range roles {
			id := rolePrefix + r.Type
			b.Entitlement(connector.Entitlement{NativeID: id, Name: cmp.Or(r.Label, r.Type) + " (admin role)"})
			b.Grant(u.ID, id)
		}
	}
	return nil
}

// groupID refuses changes to what is not a group.
func groupID(entitlementID string) (string, error) {
	switch {
	case strings.HasPrefix(entitlementID, appPrefix):
		return "", errors.New("Grantra changes Okta groups, not direct application assignments: assign the application to a group in Okta and request the group")
	case strings.HasPrefix(entitlementID, rolePrefix):
		return "", errors.New("Grantra does not change Okta admin roles: change them in the Okta admin console")
	}
	return url.PathEscape(entitlementID), nil
}

// AddMember adds a user to a group.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	g, err := groupID(entitlementID)
	if err != nil {
		return err
	}
	_, err = c.api.Do(ctx, http.MethodPut, "/groups/"+g+"/users/"+url.PathEscape(accountID), nil, nil)
	return err
}

// RemoveMember takes a user out of a group.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	g, err := groupID(entitlementID)
	if err != nil {
		return err
	}
	_, err = c.api.Do(ctx, http.MethodDelete, "/groups/"+g+"/users/"+url.PathEscape(accountID), nil, nil)
	if httpapi.HasStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// SetActive suspends a user, or lifts the suspension. Deactivated users are
// reactivated in Okta, which sends them an activation email.
func (c *Client) SetActive(ctx context.Context, accountID string, on bool) error {
	var u user
	if _, err := c.api.Get(ctx, "/users/"+url.PathEscape(accountID), &u); err != nil {
		return err
	}
	var step string
	switch {
	case on && u.Status == "SUSPENDED":
		step = "unsuspend"
	case on && u.Status == "DEPROVISIONED":
		return fmt.Errorf("Okta user %s is deactivated: reactivate them in Okta", u.Profile.Login)
	case !on && active[u.Status]:
		step = "suspend"
	default:
		return nil
	}
	_, err := c.api.Do(ctx, http.MethodPost, "/users/"+url.PathEscape(accountID)+"/lifecycle/"+step, nil, nil)
	return err
}
