// Package github connects Grantra to a GitHub organization through the
// GitHub REST API (github.com or GitHub Enterprise Server). Teams and the
// owner role become entitlements; members and outside collaborators become
// accounts. When the organization signs in with SAML, each account also gets
// the email of its SAML identity, so it links to its person.
package github

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
const Kind = "github"

func init() { connector.Register(Kind, New) }

// Config is the GitHub connector's settings.
type Config struct {
	// Organization is the organization's login, e.g. acme.
	Organization string `json:"organization"`
	// Token is a reference to a token of an organization owner (classic
	// with read:org, or fine-grained with read access to members).
	Token string `json:"token"`
	// APIURL is for GitHub Enterprise Server, e.g.
	// https://github.example.com/api/v3. Default api.github.com.
	APIURL    string `json:"api_url,omitempty"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

// Client reads and changes one organization.
type Client struct {
	api *httpapi.Client
	// login is the organization's login, org its path in the REST API.
	login, org string
}

// New builds a GitHub connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Organization == "" || strings.ContainsAny(cfg.Organization, "/?#") {
		return nil, errors.New("github config: organization must be the organization's login, e.g. acme")
	}
	api, err := httpapi.BaseURL(Kind, "api_url", cmp.Or(cfg.APIURL, "https://api.github.com"), cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	token, err := connector.Secret(Kind, "token", cfg.Token, "env:GRANTRA_SECRET_GITHUB_TOKEN", secrets)
	if err != nil {
		return nil, err
	}
	c := httpapi.New(api, httpapi.Bearer(token))
	c.Accept = "application/vnd.github+json"
	return &Client{api: c, login: cfg.Organization, org: "/orgs/" + url.PathEscape(cfg.Organization)}, nil
}

// Native id prefixes of entitlements; teams are "team:<slug>".
const (
	teamPrefix = "team:"
	// Owner is the native id of the organization owner role.
	Owner = "role:owner"
)

type user struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type team struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Read returns the organization's teams, owners, members and outside
// collaborators.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var b connector.Builder
	b.Entitlement(connector.Entitlement{NativeID: Owner, Name: "Organization owner", Description: "Full control of the organization"})
	teams, err := httpapi.AllLinkPages[team](ctx, c.api, c.org+"/teams?per_page=100")
	if err != nil {
		return connector.Snapshot{}, err
	}
	for _, t := range teams {
		id := teamPrefix + t.Slug
		b.Entitlement(connector.Entitlement{NativeID: id, Name: t.Name, Description: t.Description})
		// Members of child teams are members of their parent team too.
		err := httpapi.LinkPages(ctx, c.api, c.org+"/teams/"+url.PathEscape(t.Slug)+"/members?per_page=100&role=all",
			func(u user) { b.Grant(nativeID(u), id) })
		if err != nil {
			return connector.Snapshot{}, err
		}
	}
	if err := httpapi.LinkPages(ctx, c.api, c.org+"/members?per_page=100&role=admin", func(u user) { b.Grant(nativeID(u), Owner) }); err != nil {
		return connector.Snapshot{}, err
	}
	emails, err := c.samlEmails(ctx)
	if err != nil {
		return connector.Snapshot{}, err
	}
	add := func(kind string) func(user) {
		return func(u user) {
			b.Account(connector.Account{
				NativeID: nativeID(u), Username: u.Login, Email: emails[u.Login], Active: true,
				Attributes: map[string]string{"membership": kind},
			})
		}
	}
	if err := httpapi.LinkPages(ctx, c.api, c.org+"/members?per_page=100", add("member")); err != nil {
		return connector.Snapshot{}, err
	}
	if err := httpapi.LinkPages(ctx, c.api, c.org+"/outside_collaborators?per_page=100", add("outside collaborator")); err != nil {
		return connector.Snapshot{}, err
	}
	return b.Snapshot(), nil
}

func nativeID(u user) string { return strconv.FormatInt(u.ID, 10) }

// samlQuery reads the SAML identities of an organization, a hundred at a
// time.
const samlQuery = `query($org: String!, $after: String) {
  organization(login: $org) {
    samlIdentityProvider {
      externalIdentities(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { samlIdentity { nameId } user { login } }
      }
    }
  }
}`

// samlEmails maps logins to the email of their SAML identity. It is empty
// when the organization has no SAML sign-in, or when the token cannot see it
// (that needs an owner's token with admin:org): accounts then link to people
// by hand.
func (c *Client) samlEmails(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	var after *string
	for range httpapi.MaxPages {
		var resp struct {
			Data struct {
				Organization *struct {
					SAML *struct {
						Identities struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								SAML *struct {
									NameID string `json:"nameId"`
								} `json:"samlIdentity"`
								User *struct {
									Login string `json:"login"`
								} `json:"user"`
							} `json:"nodes"`
						} `json:"externalIdentities"`
					} `json:"samlIdentityProvider"`
				} `json:"organization"`
			} `json:"data"`
			Errors []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		_, err := c.api.Do(ctx, http.MethodPost, c.graphQL(), map[string]any{
			"query": samlQuery, "variables": map[string]any{"org": c.login, "after": after},
		}, &resp)
		if httpapi.HasStatus(err, http.StatusNotFound, http.StatusForbidden) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(resp.Errors) > 0 || resp.Data.Organization == nil || resp.Data.Organization.SAML == nil {
			return out, nil
		}
		ids := resp.Data.Organization.SAML.Identities
		for _, n := range ids.Nodes {
			if n.SAML != nil && n.User != nil && strings.Contains(n.SAML.NameID, "@") {
				out[n.User.Login] = n.SAML.NameID
			}
		}
		if !ids.PageInfo.HasNextPage {
			return out, nil
		}
		after = &ids.PageInfo.EndCursor
	}
	return nil, fmt.Errorf("SAML identities: more than %d pages", httpapi.MaxPages)
}

// graphQL is the GraphQL endpoint: /graphql next to the REST API on
// github.com, /api/graphql on GitHub Enterprise Server, whose REST API is
// under /api/v3.
func (c *Client) graphQL() string {
	if base, ok := strings.CutSuffix(c.api.Base.Path, "/v3"); ok {
		u := *c.api.Base
		u.Path, u.RawPath = base+"/graphql", ""
		return u.String()
	}
	return "/graphql"
}

// login finds the current login of an account, which the API needs to
// change memberships; logins can change, ids cannot.
func (c *Client) userLogin(ctx context.Context, accountID string) (string, error) {
	var u user
	if _, err := c.api.Get(ctx, "/user/"+url.PathEscape(accountID), &u); err != nil {
		return "", err
	}
	return url.PathEscape(u.Login), nil
}

// role is the user's role in the organization ("admin" or "member"), or ""
// when they are not a member (an outside collaborator, or nobody).
func (c *Client) role(ctx context.Context, login string) (string, error) {
	var m struct {
		Role  string `json:"role"`
		State string `json:"state"`
	}
	_, err := c.api.Get(ctx, c.org+"/memberships/"+login, &m)
	if httpapi.HasStatus(err, http.StatusNotFound) {
		return "", nil
	}
	return m.Role, err
}

func (c *Client) setRole(ctx context.Context, login, role string) error {
	_, err := c.api.Do(ctx, http.MethodPut, c.org+"/memberships/"+login, map[string]string{"role": role}, nil)
	return err
}

func teamPath(c *Client, entitlementID, login string) (string, error) {
	slug, ok := strings.CutPrefix(entitlementID, teamPrefix)
	if !ok {
		return "", fmt.Errorf("unknown GitHub entitlement %q", entitlementID)
	}
	return c.org + "/teams/" + url.PathEscape(slug) + "/memberships/" + login, nil
}

// AddMember adds a user to a team, or makes a member an organization owner.
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	login, err := c.userLogin(ctx, accountID)
	if err != nil {
		return err
	}
	if entitlementID == Owner {
		role, err := c.role(ctx, login)
		if err != nil || role == "admin" {
			return err
		}
		if role == "" {
			return errors.New("only members of the organization can become owners: invite them first")
		}
		return c.setRole(ctx, login, "admin")
	}
	path, err := teamPath(c, entitlementID, login)
	if err != nil {
		return err
	}
	_, err = c.api.Do(ctx, http.MethodPut, path, map[string]string{"role": "member"}, nil)
	return err
}

// RemoveMember takes a user out of a team, or makes an owner a plain
// member.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	login, err := c.userLogin(ctx, accountID)
	if err != nil {
		return err
	}
	if entitlementID == Owner {
		role, err := c.role(ctx, login)
		if err != nil || role != "admin" {
			return err
		}
		return c.setRole(ctx, login, "member")
	}
	path, err := teamPath(c, entitlementID, login)
	if err != nil {
		return err
	}
	_, err = c.api.Do(ctx, http.MethodDelete, path, nil, nil)
	return httpapi.Ignore(err, http.StatusNotFound)
}

// SetActive(false) removes a user from the organization (members and
// outside collaborators alike): GitHub accounts belong to their people, so
// taking someone out of the organization is how their access ends. Accounts
// read from GitHub are always active, so enabling has nothing to do; Grantra
// does not invite people.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	if active {
		return nil
	}
	login, err := c.userLogin(ctx, accountID)
	if err != nil {
		return err
	}
	role, err := c.role(ctx, login)
	if err != nil {
		return err
	}
	if role != "" {
		_, err = c.api.Do(ctx, http.MethodDelete, c.org+"/memberships/"+login, nil, nil)
	} else {
		_, err = c.api.Do(ctx, http.MethodDelete, c.org+"/outside_collaborators/"+login, nil, nil)
	}
	return httpapi.Ignore(err, http.StatusNotFound)
}
