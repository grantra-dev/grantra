package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// azureConsole assigns the person's Microsoft Entra user the target's role
// on its scope (the address) for the session, and removes the assignment
// when it ends: Azure role assignments do not expire on their own (outside
// Privileged Identity Management), so EndSessions takes it back. Grantra
// signs in as an app registration (the vault account: its client ID and a
// client secret) in the target's directory, or without one with its own
// Azure identity (a managed identity, say). It needs to assign roles on the
// scope (such as Role Based Access Control Administrator) and to read
// users in Microsoft Graph (User.Read.All), to find the person by email.
type azureConsole struct {
	c                *Consoles
	t                gateways.Target
	clientID, secret string
}

const (
	azureAPIVersion = "2022-04-01"
	azureARMScope   = "https://management.azure.com/.default"
	azureGraphScope = "https://graph.microsoft.com/.default"
)

// azureGrant is what an Azure console session granted: a role assignment,
// by its resource id.
type azureGrant struct {
	Assignment string `json:"assignment"`
}

func (a *azureConsole) endpoint(set, def string) string {
	if set != "" {
		return strings.TrimSuffix(set, "/")
	}
	return def
}

// token returns a token source for scope.
func (a *azureConsole) token(ctx context.Context, scope string) (oauth2.TokenSource, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, a.c.httpClient())
	if a.clientID != "" {
		login := a.endpoint(a.c.Endpoints.AzureLogin, "https://login.microsoftonline.com")
		cfg := clientcredentials.Config{ClientID: a.clientID, ClientSecret: a.secret, Scopes: []string{scope},
			TokenURL: login + "/" + url.PathEscape(a.t.CloudDirectory) + "/oauth2/v2.0/token", AuthStyle: oauth2.AuthStyleInParams}
		return cfg.TokenSource(ctx), nil
	}
	if a.c.Endpoints.NoDefaultCredentials {
		return nil, errors.New(a.t.Name + " has no app to sign in with, and Grantra uses no credentials of its own here")
	}
	var opts *azidentity.DefaultAzureCredentialOptions
	if a.t.CloudDirectory != "" {
		opts = &azidentity.DefaultAzureCredentialOptions{TenantID: a.t.CloudDirectory}
	}
	cred, err := azidentity.NewDefaultAzureCredential(opts)
	if err != nil {
		return nil, fmt.Errorf("Grantra has no Azure identity of its own: %w", err)
	}
	return oauth2.ReuseTokenSource(nil, azureTokens{ctx: ctx, cred: cred, scope: scope}), nil
}

type azureTokens struct {
	ctx   context.Context
	cred  *azidentity.DefaultAzureCredential
	scope string
}

func (t azureTokens) Token() (*oauth2.Token, error) {
	tok, err := t.cred.GetToken(t.ctx, policy.TokenRequestOptions{Scopes: []string{t.scope}})
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: tok.Token, TokenType: "Bearer", Expiry: tok.ExpiresOn}, nil
}

// do calls an Azure API with a token for scope, decoding the answer into
// out (when there is one). It returns the HTTP status and the error code
// Azure gave.
func (a *azureConsole) do(ctx context.Context, scope, method, rawURL string, body, out any) (int, string, error) {
	ts, err := a.token(ctx, scope)
	if err != nil {
		return 0, "", err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok, err := ts.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			msg := strings.TrimSpace(re.ErrorCode + " " + re.ErrorDescription)
			if msg == "" && re.Response != nil {
				msg = re.Response.Status
			}
			return 0, "", &Refused{"Microsoft Entra refused the app's sign-in: " + msg}
		}
		return 0, "", err
	}
	tok.SetAuthHeader(req)
	resp, err := a.c.httpClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string }
		}
		json.Unmarshal(data, &e) //nolint:errcheck
		return resp.StatusCode, e.Error.Code, &Refused{fmt.Sprintf("Azure refused: %s %s", e.Error.Code, e.Error.Message)}
	}
	if out != nil && len(data) > 0 {
		return resp.StatusCode, "", json.Unmarshal(data, out)
	}
	return resp.StatusCode, "", nil
}

func (a *azureConsole) arm(path string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("api-version", azureAPIVersion)
	return a.endpoint(a.c.Endpoints.AzureManagement, "https://management.azure.com") + a.t.Address + path + "?" + q.Encode()
}

var azureGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// odataQuote quotes s for an OData filter.
func odataQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// principal finds the person's Microsoft Entra user: by user principal
// name, then by mail.
func (a *azureConsole) principal(ctx context.Context, email string) (string, error) {
	graph := a.endpoint(a.c.Endpoints.MicrosoftGraph, "https://graph.microsoft.com")
	var user struct{ ID string }
	code, _, err := a.do(ctx, azureGraphScope, http.MethodGet, graph+"/v1.0/users/"+url.PathEscape(email)+"?$select=id", nil, &user)
	if err == nil && user.ID != "" {
		return user.ID, nil
	}
	if code != http.StatusNotFound {
		return "", err
	}
	var found struct{ Value []struct{ ID string } }
	q := url.Values{"$filter": {"mail eq " + odataQuote(email)}, "$select": {"id"}}
	if _, _, err := a.do(ctx, azureGraphScope, http.MethodGet, graph+"/v1.0/users?"+q.Encode(), nil, &found); err != nil {
		return "", err
	}
	if len(found.Value) != 1 {
		return "", &Refused{"Microsoft Entra has no user with the email " + email}
	}
	return found.Value[0].ID, nil
}

// roleDefinition finds the target's role, by id or by name.
func (a *azureConsole) roleDefinition(ctx context.Context) (string, error) {
	if azureGUID.MatchString(a.t.CloudRole) {
		var def struct{ ID string }
		if _, _, err := a.do(ctx, azureARMScope, http.MethodGet,
			a.arm("/providers/Microsoft.Authorization/roleDefinitions/"+a.t.CloudRole, nil), nil, &def); err != nil {
			return "", err
		}
		return def.ID, nil
	}
	var defs struct{ Value []struct{ ID string } }
	if _, _, err := a.do(ctx, azureARMScope, http.MethodGet, a.arm("/providers/Microsoft.Authorization/roleDefinitions",
		url.Values{"$filter": {"roleName eq " + odataQuote(a.t.CloudRole)}}), nil, &defs); err != nil {
		return "", err
	}
	if len(defs.Value) != 1 || defs.Value[0].ID == "" {
		return "", &Refused{"Azure has no role named " + a.t.CloudRole + " on " + a.t.Address}
	}
	return defs.Value[0].ID, nil
}

// assignmentName makes an assignment's name (a GUID) out of the session's
// grant.
func assignmentName(grant string) string {
	h := strings.TrimPrefix(grant, "grantra-")
	if len(h) != 32 {
		return h
	}
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (a *azureConsole) open(ctx context.Context, p person, until time.Time, grant string) (granted, error) {
	oid, err := a.principal(ctx, p.email)
	if err != nil {
		return granted{}, notGranted{err}
	}
	role, err := a.roleDefinition(ctx)
	if err != nil {
		return granted{}, notGranted{err}
	}
	path := "/providers/Microsoft.Authorization/roleAssignments/" + assignmentName(grant)
	body := map[string]any{"properties": map[string]string{
		"roleDefinitionId": role, "principalId": oid, "principalType": "User",
		"description": "Grantra console session for " + p.name + " until " + until.UTC().Format(time.RFC3339),
	}}
	g := granted{url: a.url(p), account: oid}
	status, code, err := a.do(ctx, azureARMScope, http.MethodPut, a.arm(path, nil), body, nil)
	switch {
	case code == "RoleAssignmentExists":
		// They hold the role there already, not through Grantra: nothing to
		// take back.
		return g, nil
	case err != nil && definite(status):
		return granted{}, notGranted{err}
	case err != nil:
		return granted{}, err
	}
	b, err := json.Marshal(azureGrant{Assignment: a.t.Address + path})
	if err != nil {
		return granted{}, err
	}
	g.grant = string(b)
	return g, nil
}

func (a *azureConsole) url(person) string {
	dir := ""
	if a.t.CloudDirectory != "" {
		dir = "@" + a.t.CloudDirectory
	}
	return "https://portal.azure.com/#" + dir + "/resource" + a.t.Address
}

// close removes the session's role assignment.
func (a *azureConsole) close(ctx context.Context, grant string) error {
	var gr azureGrant
	if json.Unmarshal([]byte(grant), &gr) != nil || gr.Assignment == "" {
		// Opening failed before saying what it granted: grant is the name
		// it was going to use.
		gr.Assignment = a.t.Address + "/providers/Microsoft.Authorization/roleAssignments/" + assignmentName(grant)
	}
	u := a.endpoint(a.c.Endpoints.AzureManagement, "https://management.azure.com") + gr.Assignment + "?api-version=" + azureAPIVersion
	status, _, err := a.do(ctx, azureARMScope, http.MethodDelete, u, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
