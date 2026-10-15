package grantralogin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The connector reads and provisions people. The rest of this file manages
// Grantra Login itself from the console: the apps that sign in through it and
// how people sign in. These calls need more than manage-users: manage-clients
// for apps, and manage-realm for sign-in policies.

// Status is what the console shows about the realm.
type Status struct {
	Realm       string `json:"realm"`
	DisplayName string `json:"display_name"`
	LoginTheme  string `json:"login_theme"`
	// URL is where people sign in, as Grantra reaches it.
	URL string `json:"url"`
}

// Status reads the realm.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var r struct {
		Realm       string `json:"realm"`
		DisplayName string `json:"displayName"`
		LoginTheme  string `json:"loginTheme"`
	}
	if _, err := c.do(ctx, http.MethodGet, "", nil, &r); err != nil {
		return Status{}, err
	}
	return Status{Realm: r.Realm, DisplayName: r.DisplayName, LoginTheme: r.LoginTheme,
		URL: c.base.String() + "/realms/" + url.PathEscape(c.realm) + "/account"}, nil
}

// SSOApp is an application that signs people in through Grantra Login (an
// OpenID Connect client).
type SSOApp struct {
	ID           string   `json:"id"`
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Enabled      bool     `json:"enabled"`
	Public       bool     `json:"public"`
	RedirectURIs []string `json:"redirect_uris"`
}

// systemClients are Keycloak's own clients and Grantra's service account:
// not apps a company registered, and not for the console to change.
var systemClients = map[string]bool{
	"account": true, "account-console": true, "admin-cli": true, "broker": true,
	"realm-management": true, "security-admin-console": true, "grantra-connector": true,
}

type client struct {
	ID                        string            `json:"id,omitempty"`
	ClientID                  string            `json:"clientId"`
	Name                      string            `json:"name,omitempty"`
	Description               string            `json:"description,omitempty"`
	Protocol                  string            `json:"protocol,omitempty"`
	Enabled                   bool              `json:"enabled"`
	PublicClient              bool              `json:"publicClient"`
	BearerOnly                bool              `json:"bearerOnly"`
	StandardFlowEnabled       bool              `json:"standardFlowEnabled"`
	DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
	ServiceAccountsEnabled    bool              `json:"serviceAccountsEnabled"`
	RedirectURIs              []string          `json:"redirectUris"`
	WebOrigins                []string          `json:"webOrigins,omitempty"`
	Attributes                map[string]string `json:"attributes,omitempty"`
}

func (cl client) app() SSOApp {
	return SSOApp{ID: cl.ID, ClientID: cl.ClientID, Name: cl.Name, Description: cl.Description,
		Enabled: cl.Enabled, Public: cl.PublicClient, RedirectURIs: nonNil(cl.RedirectURIs)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Apps lists the OpenID Connect apps registered in the realm.
func (c *Client) Apps(ctx context.Context) ([]SSOApp, error) {
	out := []SSOApp{}
	err := list(ctx, c, "/clients", nil, func(cl client) {
		if systemClients[cl.ClientID] || cl.Protocol == "saml" || cl.BearerOnly {
			return
		}
		out = append(out, cl.app())
	})
	return out, err
}

// NewSSOApp describes an app to register.
type NewSSOApp struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Public       bool     `json:"public"`
	RedirectURIs []string `json:"redirect_uris"`
}

// InvalidError is a request the console should answer with 400.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error { return &InvalidError{fmt.Sprintf(format, args...)} }

// checkRedirectURI accepts https URLs, and http only on localhost for
// development. Wildcards are refused: a redirect URI that matches more than
// the app is how sign-in codes get stolen.
func checkRedirectURI(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Fragment != "" || strings.Contains(s, "*") {
		return invalid("redirect URI %q must be an absolute URL without wildcards or fragment", s)
	}
	host := u.Hostname()
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1"):
	default:
		return invalid("redirect URI %q must use https (http only for localhost)", s)
	}
	return nil
}

// CreateApp registers an app that signs people in with the authorization
// code flow and PKCE. A confidential app's secret is returned once.
func (c *Client) CreateApp(ctx context.Context, in NewSSOApp) (SSOApp, string, error) {
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.ClientID == "" || len(in.ClientID) > 100 || strings.ContainsAny(in.ClientID, " /?#"):
		return SSOApp{}, "", invalid("client_id is required: letters, digits, dots, dashes, up to 100 characters")
	case systemClients[in.ClientID]:
		return SSOApp{}, "", invalid("client_id %q is reserved", in.ClientID)
	case in.Name == "":
		return SSOApp{}, "", invalid("name is required")
	case len(in.RedirectURIs) == 0:
		return SSOApp{}, "", invalid("at least one redirect URI is required")
	}
	origins := []string{}
	for _, r := range in.RedirectURIs {
		if err := checkRedirectURI(r); err != nil {
			return SSOApp{}, "", err
		}
		u, _ := url.Parse(r)
		if o := u.Scheme + "://" + u.Host; !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	cl := client{
		ClientID: in.ClientID, Name: in.Name, Description: in.Description, Protocol: "openid-connect",
		Enabled: true, PublicClient: in.Public, StandardFlowEnabled: true,
		RedirectURIs: in.RedirectURIs, WebOrigins: origins,
		Attributes: map[string]string{"pkce.code.challenge.method": "S256", "post.logout.redirect.uris": "+"},
	}
	hdr, err := c.do(ctx, http.MethodPost, "/clients", cl, nil)
	if hasStatus(err, http.StatusConflict) {
		return SSOApp{}, "", invalid("an app with client_id %q already exists", in.ClientID)
	}
	if err != nil {
		return SSOApp{}, "", err
	}
	loc := hdr.Get("Location")
	cl.ID = loc[strings.LastIndex(loc, "/")+1:]
	if cl.PublicClient {
		return cl.app(), "", nil
	}
	var s struct {
		Value string `json:"value"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/clients/"+url.PathEscape(cl.ID)+"/client-secret", nil, &s); err != nil {
		return SSOApp{}, "", err
	}
	return cl.app(), s.Value, nil
}

// RotateSecret gives a confidential app a new secret, returned once. The old
// one stops working at once.
func (c *Client) RotateSecret(ctx context.Context, id string) (string, error) {
	var cl client
	if _, err := c.do(ctx, http.MethodGet, "/clients/"+url.PathEscape(id), nil, &cl); err != nil {
		return "", err
	}
	if systemClients[cl.ClientID] {
		return "", invalid("client %q is managed by Keycloak or Grantra", cl.ClientID)
	}
	if cl.PublicClient {
		return "", invalid("public apps have no secret")
	}
	var s struct {
		Value string `json:"value"`
	}
	_, err := c.do(ctx, http.MethodPost, "/clients/"+url.PathEscape(id)+"/client-secret", nil, &s)
	return s.Value, err
}

// Second factor policies.
const (
	// MFAOptional asks for a second factor only from people who set one up.
	// It is Keycloak's own default.
	MFAOptional = "optional"
	// MFARequired asks everyone; people without a factor set one up at their
	// next sign-in.
	MFARequired = "required"
	// MFASensitive asks the people who hold sensitive access, whom Grantra
	// keeps in the MFARole realm role, and whoever set one up.
	MFASensitive = "sensitive"
)

// MFARole is the realm role Grantra gives the people who must use a second
// factor under MFASensitive.
const MFARole = "grantra-mfa-required"

// Policy is how people sign in to Grantra Login.
type Policy struct {
	MFA string `json:"mfa"`
	// Passkeys lets people sign in with a passkey instead of a password, and
	// register one from their account page.
	Passkeys bool `json:"passkeys"`
}

// flowAlias is the browser flow Grantra writes. Grantra owns it: it is
// rebuilt on every policy change, so edits made in Keycloak are lost.
const flowAlias = "grantra browser"

type realmPolicy struct {
	BrowserFlow string `json:"browserFlow,omitempty"`
	Passkeys    *bool  `json:"webAuthnPolicyPasswordlessPasskeysEnabled,omitempty"`
}

type execution struct {
	ID                   string `json:"id"`
	Requirement          string `json:"requirement"`
	DisplayName          string `json:"displayName"`
	ProviderID           string `json:"providerId"`
	Level                int    `json:"level"`
	AuthenticationFlow   bool   `json:"authenticationFlow"`
	AuthenticationConfig string `json:"authenticationConfig,omitempty"`
	Priority             int    `json:"priority"`
	Index                int    `json:"index"`
}

// Policy reads the current sign-in policy.
func (c *Client) Policy(ctx context.Context) (Policy, error) {
	var r realmPolicy
	if _, err := c.do(ctx, http.MethodGet, "", nil, &r); err != nil {
		return Policy{}, err
	}
	p := Policy{MFA: MFAOptional, Passkeys: r.Passkeys != nil && *r.Passkeys}
	if r.BrowserFlow != flowAlias {
		return p, nil
	}
	var exs []execution
	if _, err := c.do(ctx, http.MethodGet, "/authentication/flows/"+url.PathEscape(flowAlias)+"/executions", nil, &exs); err != nil {
		return Policy{}, err
	}
	p.MFA = MFARequired
	for _, e := range exs {
		if e.ProviderID == "conditional-user-role" {
			p.MFA = MFASensitive
		}
	}
	return p, nil
}

// SetPolicy writes the sign-in policy: it rebuilds Grantra's browser flow and
// binds it to the realm.
func (c *Client) SetPolicy(ctx context.Context, p Policy) error {
	if p.MFA != MFAOptional && p.MFA != MFARequired && p.MFA != MFASensitive {
		return invalid("mfa must be %s, %s or %s", MFAOptional, MFARequired, MFASensitive)
	}
	if p.MFA == MFASensitive {
		if err := c.ensureRole(ctx, MFARole, "People Grantra asks for a second factor because they hold sensitive access. Managed by Grantra."); err != nil {
			return err
		}
	}
	if err := c.setPasskeys(ctx, p.Passkeys); err != nil {
		return err
	}
	// A flow in use cannot be deleted: fall back to Keycloak's own browser
	// flow while Grantra's is rebuilt.
	if _, err := c.do(ctx, http.MethodPut, "", realmPolicy{BrowserFlow: "browser"}, nil); err != nil {
		return err
	}
	var flows []struct {
		ID    string `json:"id"`
		Alias string `json:"alias"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/authentication/flows", nil, &flows); err != nil {
		return err
	}
	for _, f := range flows {
		if f.Alias == flowAlias {
			if _, err := c.do(ctx, http.MethodDelete, "/authentication/flows/"+url.PathEscape(f.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	if p.MFA == MFAOptional {
		// Keycloak's browser flow already asks for a factor when one is set
		// up; keep it rather than a copy of it.
		return nil
	}
	if err := c.buildFlow(ctx, p.MFA); err != nil {
		return fmt.Errorf("build the sign-in flow: %w", err)
	}
	_, err := c.do(ctx, http.MethodPut, "", realmPolicy{BrowserFlow: flowAlias}, nil)
	return err
}

// setPasskeys turns passkey sign-in on or off. Keycloak only takes the
// setting along with the rest of the passwordless WebAuthn policy, so the
// policy is read and written back whole.
func (c *Client) setPasskeys(ctx context.Context, on bool) error {
	var realm map[string]any
	if _, err := c.do(ctx, http.MethodGet, "", nil, &realm); err != nil {
		return err
	}
	policy := map[string]any{}
	for k, v := range realm {
		if strings.HasPrefix(k, "webAuthnPolicyPasswordless") {
			policy[k] = v
		}
	}
	policy["webAuthnPolicyPasswordlessPasskeysEnabled"] = on
	_, err := c.do(ctx, http.MethodPut, "", policy, nil)
	return err
}

// buildFlow writes Grantra's browser flow as a copy of Keycloak's own, so it
// keeps everything else Keycloak does there (cookies, identity providers,
// organizations, skipping the second factor after a passkey). Then, in the
// copy's "Conditional 2FA" step:
//
//   - MFARequired drops the "user configured" condition, so everyone is asked;
//     people without a factor set one up.
//   - MFASensitive replaces it with "has the MFARole role", and adds a second
//     step that keeps asking everyone else who set up a factor.
//
// The asked-for factor is a one-time code from an authenticator app: Keycloak
// only walks people through setting up a factor they lack when that factor is
// required, not when it is one of several alternatives. Signing in with a
// passkey (see Policy.Passkeys) skips the step, as in Keycloak's own flow.
func (c *Client) buildFlow(ctx context.Context, mode string) error {
	if _, err := c.do(ctx, http.MethodPost, "/authentication/flows/browser/copy", map[string]string{"newName": flowAlias}, nil); err != nil {
		return err
	}
	flowPath := func(alias string) string { return "/authentication/flows/" + url.PathEscape(alias) + "/executions" }
	var exs []execution
	if _, err := c.do(ctx, http.MethodGet, flowPath(flowAlias), nil, &exs); err != nil {
		return err
	}
	// Find the 2FA step, the forms flow holding it, and the 2FA step's own
	// executions.
	at := slices.IndexFunc(exs, func(e execution) bool {
		return e.AuthenticationFlow && strings.HasSuffix(e.DisplayName, "Conditional 2FA")
	})
	if at < 0 {
		return errors.New(`Keycloak's browser flow has no "Conditional 2FA" step to build on`)
	}
	twoFA := exs[at]
	forms := ""
	for i := at - 1; i >= 0; i-- {
		if exs[i].Level == twoFA.Level-1 && exs[i].AuthenticationFlow {
			forms = exs[i].DisplayName
			break
		}
	}
	var children []execution
	for _, e := range exs[at+1:] {
		if e.Level <= twoFA.Level {
			break
		}
		if e.Level == twoFA.Level+1 {
			children = append(children, e)
		}
	}
	if forms == "" {
		return errors.New(`Keycloak's browser flow has no forms step around "Conditional 2FA"`)
	}

	set := func(e execution, requirement string) error {
		if e.Requirement == requirement {
			return nil
		}
		e.Requirement = requirement
		_, err := c.do(ctx, http.MethodPut, flowPath(flowAlias), e, nil)
		return err
	}
	for _, e := range children {
		var err error
		switch e.ProviderID {
		case "conditional-user-configured":
			err = set(e, "DISABLED")
		case "auth-otp-form":
			// Keycloak only asks people to set up a factor they lack when
			// that factor is required; among alternatives it fails instead.
			err = set(e, "REQUIRED")
		case "webauthn-authenticator":
			err = set(e, "DISABLED")
		}
		if err != nil {
			return err
		}
	}
	if mode != MFASensitive {
		return nil
	}

	// Sensitive access: "has the role" in the 2FA step...
	if err := c.addCondition(ctx, twoFA.DisplayName, 5, false); err != nil {
		return err
	}
	// ...and a step for everyone else who set up a factor.
	others := flowAlias + " optional second factor"
	if _, err := c.do(ctx, http.MethodPost, flowPath(forms)+"/flow", map[string]any{
		"alias": others, "type": "basic-flow", "provider": "registration-page-form", "priority": twoFA.Priority + 5,
		"description": "People without sensitive access who set up a second factor",
	}, nil); err != nil {
		return err
	}
	if _, err := c.addStep(ctx, others, "conditional-user-configured", 10, "REQUIRED"); err != nil {
		return err
	}
	if err := c.addCondition(ctx, others, 20, true); err != nil {
		return err
	}
	for i, p := range []string{"auth-otp-form", "webauthn-authenticator"} {
		if _, err := c.addStep(ctx, others, p, 30+10*i, "ALTERNATIVE"); err != nil {
			return err
		}
	}
	if _, err := c.do(ctx, http.MethodGet, flowPath(forms), nil, &exs); err != nil {
		return err
	}
	for _, e := range exs {
		if e.AuthenticationFlow && e.DisplayName == others {
			return set(e, "CONDITIONAL")
		}
	}
	return fmt.Errorf("step %q not found after creating it", others)
}

// addStep adds an execution to a flow at a priority, with a requirement, and
// returns its id.
func (c *Client) addStep(ctx context.Context, flow, provider string, priority int, requirement string) (string, error) {
	path := "/authentication/flows/" + url.PathEscape(flow) + "/executions"
	if _, err := c.do(ctx, http.MethodPost, path+"/execution", map[string]any{"provider": provider, "priority": priority}, nil); err != nil {
		return "", err
	}
	var exs []execution
	if _, err := c.do(ctx, http.MethodGet, path, nil, &exs); err != nil {
		return "", err
	}
	for _, e := range exs {
		if e.Level == 0 && e.ProviderID == provider && e.Priority == priority {
			if e.Requirement != requirement {
				e.Requirement = requirement
				if _, err := c.do(ctx, http.MethodPut, path, e, nil); err != nil {
					return "", err
				}
			}
			return e.ID, nil
		}
	}
	return "", fmt.Errorf("%s not found in %q after adding it", provider, flow)
}

// addCondition adds "the user has (or, negated, has not) the MFARole role".
func (c *Client) addCondition(ctx context.Context, flow string, priority int, negate bool) error {
	id, err := c.addStep(ctx, flow, "conditional-user-role", priority, "REQUIRED")
	if err != nil {
		return err
	}
	alias := "grantra has sensitive access"
	if negate {
		alias = "grantra has no sensitive access"
	}
	_, err = c.do(ctx, http.MethodPost, "/authentication/executions/"+url.PathEscape(id)+"/config", map[string]any{
		"alias":  alias,
		"config": map[string]string{"condUserRole": MFARole, "negate": strconv.FormatBool(negate)},
	}, nil)
	return err
}

// ensureRole creates a realm role unless it exists.
func (c *Client) ensureRole(ctx context.Context, name, description string) error {
	_, err := c.do(ctx, http.MethodPost, "/roles", map[string]string{"name": name, "description": description}, nil)
	if hasStatus(err, http.StatusConflict) {
		return nil
	}
	return err
}

// RoleHolders returns the ids of the users who hold a realm role directly.
func (c *Client) RoleHolders(ctx context.Context, role string) ([]string, error) {
	var ids []string
	err := list(ctx, c, "/roles/"+url.PathEscape(role)+"/users", url.Values{"briefRepresentation": {"true"}}, func(u user) {
		ids = append(ids, u.ID)
	})
	if hasStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	return ids, err
}

// IsForbidden reports whether err is Keycloak refusing the service account,
// which means it lacks a realm-management role.
func IsForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

// IsNotFound reports whether err is a 404 from the Admin API.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }
