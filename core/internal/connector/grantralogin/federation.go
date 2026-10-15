package grantralogin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Federation lets people sign in to Grantra Login with the identity provider
// their company already has (Keycloak identity brokering), and routes sign-in
// by email domain so a company can move to Grantra Login one domain at a
// time. These calls need the realm-management role
// manage-identity-providers, and manage-realm for routing.

// Identity provider kinds.
const (
	KindEntra = "entra"
	KindOkta  = "okta"
	KindOIDC  = "oidc"
	KindSAML  = "saml"
)

// IdentityProvider is an external identity provider people can sign in with.
type IdentityProvider struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	// LinkByEmail signs people whose email is already in Grantra Login in to
	// that account, without asking them for its password first.
	LinkByEmail bool `json:"link_by_email"`
	// Domains are the email domains whose people are sent straight to this
	// provider.
	Domains []string `json:"domains"`
	// RedirectURI is what to register at the provider: the OpenID Connect
	// redirect URI, or the SAML assertion consumer service.
	RedirectURI string `json:"redirect_uri"`
	// SAMLMetadata is Grantra Login's SAML service provider metadata, for
	// SAML providers.
	SAMLMetadata string `json:"saml_metadata,omitempty"`
}

// NewIdentityProvider describes an identity provider to add.
type NewIdentityProvider struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	// Tenant is the Entra ID tenant: its id or a domain it owns.
	Tenant string `json:"tenant,omitempty"`
	// Domain is the Okta org, such as acme.okta.com.
	Domain string `json:"domain,omitempty"`
	// Issuer is a generic OpenID Connect provider's issuer URL.
	Issuer       string `json:"issuer,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	// MetadataURL is a SAML provider's metadata.
	MetadataURL string `json:"metadata_url,omitempty"`
	// EmailAttribute is the SAML attribute holding the email.
	EmailAttribute string `json:"email_attribute,omitempty"`
	// LinkByEmail defaults to true.
	LinkByEmail *bool `json:"link_by_email,omitempty"`
}

// linkFlowAlias is the first broker login flow Grantra writes for providers
// that link by email: Keycloak's own, with "link to the account with this
// email" in place of "confirm with the account's password".
const linkFlowAlias = "grantra first broker login"

// orgPrefix names the organizations Grantra keeps to route domains, one per
// identity provider.
const orgPrefix = "grantra-"

// kindKey records the kind in the provider's config.
const kindKey = "grantra.kind"

type idp struct {
	Alias                     string            `json:"alias"`
	DisplayName               string            `json:"displayName,omitempty"`
	ProviderID                string            `json:"providerId"`
	Enabled                   bool              `json:"enabled"`
	TrustEmail                bool              `json:"trustEmail"`
	FirstBrokerLoginFlowAlias string            `json:"firstBrokerLoginFlowAlias,omitempty"`
	OrganizationID            string            `json:"organizationId,omitempty"`
	Config                    map[string]string `json:"config"`
}

type organization struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Alias   string   `json:"alias,omitempty"`
	Enabled bool     `json:"enabled"`
	Domains []domain `json:"domains"`
}

type domain struct {
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
}

var aliasRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}$`)

// IdentityProviders lists the realm's identity providers.
func (c *Client) IdentityProviders(ctx context.Context) ([]IdentityProvider, error) {
	var ps []idp
	if _, err := c.do(ctx, http.MethodGet, "/identity-provider/instances", nil, &ps); err != nil {
		return nil, err
	}
	orgs, err := c.grantraOrgs(ctx)
	if err != nil {
		return nil, err
	}
	out := []IdentityProvider{}
	for _, p := range ps {
		kind := p.Config[kindKey]
		if kind == "" {
			kind = p.ProviderID
		}
		ip := IdentityProvider{
			Alias: p.Alias, DisplayName: p.DisplayName, Kind: kind, Enabled: p.Enabled,
			LinkByEmail: p.FirstBrokerLoginFlowAlias == linkFlowAlias,
			Domains:     []string{},
			RedirectURI: c.brokerURL(p.Alias),
		}
		if o, ok := orgs[p.Alias]; ok {
			for _, d := range o.Domains {
				ip.Domains = append(ip.Domains, d.Name)
			}
			sort.Strings(ip.Domains)
		}
		if p.ProviderID == "saml" {
			ip.SAMLMetadata = ip.RedirectURI + "/descriptor"
		}
		out = append(out, ip)
	}
	return out, nil
}

func (c *Client) brokerURL(alias string) string {
	return c.base.String() + "/realms/" + url.PathEscape(c.realm) + "/broker/" + url.PathEscape(alias) + "/endpoint"
}

// checkProviderURL accepts https URLs, and http only on localhost: Keycloak
// fetches them to learn the provider's keys.
func checkProviderURL(field, s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return invalid("%s must be an absolute URL", field)
	}
	host := u.Hostname()
	if u.Scheme != "https" && (u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1")) {
		return invalid("%s must use https (http only for localhost)", field)
	}
	return nil
}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// discovery returns where the provider's configuration is: its OpenID
// Connect discovery document, or its SAML metadata.
func discovery(in NewIdentityProvider) (string, error) {
	switch in.Kind {
	case KindEntra:
		t := strings.ToLower(strings.TrimSpace(in.Tenant))
		switch {
		case t == "":
			return "", invalid("tenant is required: the Entra ID tenant id, or a domain it owns")
		case t == "common" || t == "organizations" || t == "consumers":
			// Multi-tenant endpoints accept accounts from any tenant, whose
			// email claims nobody here vouches for.
			return "", invalid("tenant must be your own tenant, not %q", t)
		case !hostRE.MatchString(t) && !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(t):
			return "", invalid("tenant must be a tenant id or a domain")
		}
		return "https://login.microsoftonline.com/" + t + "/v2.0/.well-known/openid-configuration", nil
	case KindOkta:
		d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(in.Domain, "https://")))
		d = strings.TrimSuffix(d, "/")
		if !hostRE.MatchString(d) {
			return "", invalid("domain is required: your Okta org, such as acme.okta.com")
		}
		return "https://" + d + "/.well-known/openid-configuration", nil
	case KindOIDC:
		iss := strings.TrimSuffix(strings.TrimSpace(in.Issuer), "/")
		if err := checkProviderURL("issuer", iss); err != nil {
			return "", err
		}
		return iss + "/.well-known/openid-configuration", nil
	case KindSAML:
		m := strings.TrimSpace(in.MetadataURL)
		if err := checkProviderURL("metadata_url", m); err != nil {
			return "", err
		}
		return m, nil
	}
	return "", invalid("kind must be %s, %s, %s or %s", KindEntra, KindOkta, KindOIDC, KindSAML)
}

// CreateIdentityProvider adds an identity provider. Keycloak reads the
// provider's configuration from its discovery document or metadata, so the
// provider must be reachable from Grantra Login.
func (c *Client) CreateIdentityProvider(ctx context.Context, in NewIdentityProvider) (IdentityProvider, error) {
	in.Alias = strings.TrimSpace(in.Alias)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if !aliasRE.MatchString(in.Alias) {
		return IdentityProvider{}, invalid("alias is required: lowercase letters, digits and dashes, up to 50 characters")
	}
	if in.DisplayName == "" {
		return IdentityProvider{}, invalid("display_name is required")
	}
	from, err := discovery(in)
	if err != nil {
		return IdentityProvider{}, err
	}
	saml := in.Kind == KindSAML
	if !saml && (strings.TrimSpace(in.ClientID) == "" || in.ClientSecret == "") {
		return IdentityProvider{}, invalid("client_id and client_secret are required: register Grantra Login as an app at the provider first")
	}
	providerID := "oidc"
	if saml {
		providerID = "saml"
	}
	var cfg map[string]string
	_, err = c.do(ctx, http.MethodPost, "/identity-provider/import-config", map[string]string{"providerId": providerID, "fromUrl": from}, &cfg)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status >= 400 && ae.status < 500 && ae.status != http.StatusForbidden {
			return IdentityProvider{}, invalid("Grantra Login could not read the provider's configuration at %s: %s", from, ae.msg)
		}
		return IdentityProvider{}, err
	}
	if len(cfg) == 0 {
		return IdentityProvider{}, invalid("Grantra Login found no configuration at %s", from)
	}
	cfg[kindKey] = in.Kind
	cfg["syncMode"] = "IMPORT"
	if saml {
		// Ask for the email as the subject, and require signed answers.
		cfg["nameIDPolicyFormat"] = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"
		cfg["principalType"] = "SUBJECT"
		cfg["wantAssertionsSigned"] = "true"
		cfg["validateSignature"] = "true"
	} else {
		cfg["clientId"] = strings.TrimSpace(in.ClientID)
		cfg["clientSecret"] = in.ClientSecret
		cfg["clientAuthMethod"] = "client_secret_post"
		cfg["defaultScope"] = "openid email profile"
		cfg["pkceEnabled"] = "true"
		cfg["pkceMethod"] = "S256"
		cfg["validateSignature"] = "true"
		cfg["useJwksUrl"] = "true"
	}
	link := in.LinkByEmail == nil || *in.LinkByEmail
	p := idp{Alias: in.Alias, DisplayName: in.DisplayName, ProviderID: providerID, Enabled: true,
		// The provider vouches for the emails it sends: they count as
		// verified, which is what linking by email relies on.
		TrustEmail: link, Config: cfg}
	if link {
		if err := c.ensureLinkFlow(ctx); err != nil {
			return IdentityProvider{}, fmt.Errorf("build the account linking flow: %w", err)
		}
		p.FirstBrokerLoginFlowAlias = linkFlowAlias
	}
	_, err = c.do(ctx, http.MethodPost, "/identity-provider/instances", p, nil)
	if hasStatus(err, http.StatusConflict) {
		return IdentityProvider{}, invalid("an identity provider with alias %q already exists", in.Alias)
	}
	if err != nil {
		return IdentityProvider{}, err
	}
	if saml {
		attr := strings.TrimSpace(in.EmailAttribute)
		if attr == "" {
			attr = "email"
		}
		_, err := c.do(ctx, http.MethodPost, "/identity-provider/instances/"+url.PathEscape(in.Alias)+"/mappers", map[string]any{
			"name": "email", "identityProviderAlias": in.Alias, "identityProviderMapper": "saml-user-attribute-idp-mapper",
			"config": map[string]string{"attribute.name": attr, "user.attribute": "email", "syncMode": "INHERIT"},
		}, nil)
		if err != nil {
			return IdentityProvider{}, fmt.Errorf("map the email attribute: %w", err)
		}
	}
	out := IdentityProvider{Alias: p.Alias, DisplayName: p.DisplayName, Kind: in.Kind, Enabled: true,
		LinkByEmail: link, Domains: []string{}, RedirectURI: c.brokerURL(p.Alias)}
	if saml {
		out.SAMLMetadata = out.RedirectURI + "/descriptor"
	}
	return out, nil
}

// ensureLinkFlow writes linkFlowAlias unless it exists, as a copy of
// Keycloak's "first broker login" flow where "Handle Existing Account"
// (confirm, then prove the account is yours) gives way to "Automatically set
// existing user": someone whose provider says their email is that of an
// existing account signs in to it.
func (c *Client) ensureLinkFlow(ctx context.Context) error {
	flowPath := func(alias string) string { return "/authentication/flows/" + url.PathEscape(alias) + "/executions" }
	var exs []execution
	_, err := c.do(ctx, http.MethodGet, flowPath(linkFlowAlias), nil, &exs)
	if err == nil {
		return nil
	}
	if !hasStatus(err, http.StatusNotFound) {
		return err
	}
	if _, err := c.do(ctx, http.MethodPost, "/authentication/flows/first%20broker%20login/copy", map[string]string{"newName": linkFlowAlias}, nil); err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodGet, flowPath(linkFlowAlias), nil, &exs); err != nil {
		return err
	}
	linking := ""
	for _, e := range exs {
		switch {
		case e.AuthenticationFlow && strings.HasSuffix(e.DisplayName, "User creation or linking"):
			linking = e.DisplayName
		case e.AuthenticationFlow && strings.HasSuffix(e.DisplayName, "Handle Existing Account"):
			e.Requirement = "DISABLED"
			if _, err := c.do(ctx, http.MethodPut, flowPath(linkFlowAlias), e, nil); err != nil {
				return err
			}
		}
	}
	if linking == "" {
		return errors.New(`Keycloak's first broker login flow has no "User creation or linking" step to build on`)
	}
	_, err = c.addStep(ctx, linking, "idp-auto-link", 30, "ALTERNATIVE")
	return err
}

// DeleteIdentityProvider removes an identity provider and its domains. People
// who signed in with it keep their accounts.
func (c *Client) DeleteIdentityProvider(ctx context.Context, alias string) error {
	if err := c.SetDomains(ctx, alias, nil); err != nil {
		return err
	}
	_, err := c.do(ctx, http.MethodDelete, "/identity-provider/instances/"+url.PathEscape(alias), nil, nil)
	return err
}

// grantraOrgs returns Grantra's routing organizations by identity provider
// alias.
func (c *Client) grantraOrgs(ctx context.Context) (map[string]organization, error) {
	out := map[string]organization{}
	err := list(ctx, c, "/organizations", url.Values{"briefRepresentation": {"false"}}, func(o organization) {
		if a, ok := strings.CutPrefix(o.Alias, orgPrefix); ok {
			out[a] = o
		}
	})
	if hasStatus(err, http.StatusNotFound) || hasStatus(err, http.StatusBadRequest) {
		// Organizations are off: nothing is routed.
		return out, nil
	}
	return out, err
}

func cleanDomains(in []string) ([]string, error) {
	out := []string{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if d == "" {
			continue
		}
		if !hostRE.MatchString(d) {
			return nil, invalid("%q is not an email domain", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SetDomains sends the people of these email domains straight to the
// identity provider when they sign in. Everyone else keeps signing in with
// their Grantra Login password. No domains stops the routing.
//
// Routing uses Keycloak organizations: one per provider, holding its domains.
// Organizations make the sign-in page ask for the email first; Grantra turns
// them off again when no organization is left.
func (c *Client) SetDomains(ctx context.Context, alias string, domains []string) error {
	domains, err := cleanDomains(domains)
	if err != nil {
		return err
	}
	var p idp
	if _, err := c.do(ctx, http.MethodGet, "/identity-provider/instances/"+url.PathEscape(alias), nil, &p); err != nil {
		return err
	}
	orgs, err := c.grantraOrgs(ctx)
	if err != nil {
		return err
	}
	org, exists := orgs[alias]
	if len(domains) == 0 {
		if !exists {
			return nil
		}
		if _, err := c.do(ctx, http.MethodDelete, "/organizations/"+url.PathEscape(org.ID), nil, nil); err != nil {
			return err
		}
		return c.organizationsOff(ctx)
	}
	if _, err := c.do(ctx, http.MethodPut, "", map[string]bool{"organizationsEnabled": true}, nil); err != nil {
		return err
	}
	org.Name = "Grantra: " + p.Alias
	org.Alias = orgPrefix + p.Alias
	org.Enabled = true
	org.Domains = nil
	for _, d := range domains {
		// Verified: the admin who routes a domain is the one vouching for it.
		org.Domains = append(org.Domains, domain{Name: d, Verified: true})
	}
	if exists {
		_, err = c.do(ctx, http.MethodPut, "/organizations/"+url.PathEscape(org.ID), org, nil)
	} else {
		var hdr http.Header
		hdr, err = c.do(ctx, http.MethodPost, "/organizations", org, nil)
		if err == nil {
			loc := hdr.Get("Location")
			org.ID = loc[strings.LastIndex(loc, "/")+1:]
		}
	}
	if hasStatus(err, http.StatusConflict) || hasStatus(err, http.StatusBadRequest) {
		var ae *apiError
		errors.As(err, &ae)
		return invalid("Grantra Login refused the domains: %s (a domain can go to one provider only)", ae.msg)
	}
	if err != nil {
		return err
	}
	if p.OrganizationID != org.ID {
		if _, err := c.do(ctx, http.MethodPost, "/organizations/"+url.PathEscape(org.ID)+"/identity-providers", p.Alias, nil); err != nil {
			return err
		}
		// Linking changes the provider: read it again before updating it.
		if _, err := c.do(ctx, http.MethodGet, "/identity-provider/instances/"+url.PathEscape(alias), nil, &p); err != nil {
			return err
		}
	}
	if p.Config["kc.org.domain"] == "ANY" && p.Config["kc.org.broker.redirect.mode.email-matches"] == "true" {
		return nil
	}
	// Any of the organization's domains sends people to the provider. The
	// masked client secret is sent back as read, which keeps it.
	p.Config["kc.org.domain"] = "ANY"
	p.Config["kc.org.broker.redirect.mode.email-matches"] = "true"
	_, err = c.do(ctx, http.MethodPut, "/identity-provider/instances/"+url.PathEscape(alias), p, nil)
	return err
}

// organizationsOff turns organizations off when the realm has none left, so
// the sign-in page asks for username and password together again.
func (c *Client) organizationsOff(ctx context.Context) error {
	var orgs []organization
	if _, err := c.do(ctx, http.MethodGet, "/organizations?first=0&max=1", nil, &orgs); err != nil {
		return err
	}
	if len(orgs) > 0 {
		return nil
	}
	_, err := c.do(ctx, http.MethodPut, "", map[string]bool{"organizationsEnabled": false}, nil)
	return err
}
