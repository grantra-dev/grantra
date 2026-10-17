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
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// gcpConsole binds the person's Google account to the target's role on its
// project (the address) with an IAM condition that ends the binding with
// the session, so it ends even if Grantra cannot take it back. Grantra signs
// in with a service account key (the vault account's secret, its JSON), or
// without one with its own Google credentials; either needs to change the
// project's IAM policy.
type gcpConsole struct {
	c   *Consoles
	t   gateways.Target
	key string
}

const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// gcpGrant is what a Google Cloud console session granted.
type gcpGrant struct {
	Title string `json:"title"`
}

func (g *gcpConsole) client(ctx context.Context) (*http.Client, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.c.httpClient())
	if g.key != "" {
		cfg, err := google.JWTConfigFromJSON([]byte(g.key), gcpScope)
		if err != nil {
			return nil, errors.New("the vault account's secret is not a Google service account key (JSON)")
		}
		return cfg.Client(ctx), nil
	}
	if g.c.Endpoints.NoDefaultCredentials {
		return nil, errors.New(g.t.Name + " has no service account key, and Grantra uses no credentials of its own here")
	}
	creds, err := google.FindDefaultCredentials(ctx, gcpScope)
	if err != nil {
		return nil, fmt.Errorf("Grantra has no Google credentials of its own: %w", err)
	}
	return oauth2.NewClient(ctx, creds.TokenSource), nil
}

func (g *gcpConsole) api(path string) string {
	base := "https://cloudresourcemanager.googleapis.com"
	if g.c.Endpoints.GoogleResourceMgr != "" {
		base = g.c.Endpoints.GoogleResourceMgr
	}
	return base + "/v1/projects/" + url.PathEscape(g.t.Address) + path
}

// call posts body to the Resource Manager API and decodes its answer.
func (g *gcpConsole) call(ctx context.Context, hc *http.Client, path string, body, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.api(path), bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message, Status string }
		}
		json.Unmarshal(data, &e) //nolint:errcheck
		return resp.StatusCode, &Refused{fmt.Sprintf("Google Cloud refused to change the IAM policy of %s: %s %s",
			g.t.Address, e.Error.Status, e.Error.Message)}
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}

// change applies edit to the project's IAM policy, again if another change
// raced with it. edit returns false when there is nothing to change.
func (g *gcpConsole) change(ctx context.Context, edit func(bindings []map[string]any) ([]map[string]any, bool)) error {
	hc, err := g.client(ctx)
	if err != nil {
		return notGranted{err}
	}
	for attempt := 0; ; attempt++ {
		// Version 3 returns conditions; the rest of the policy (audit
		// settings and fields Grantra does not know) goes back untouched.
		var policy map[string]json.RawMessage
		if _, err := g.call(ctx, hc, ":getIamPolicy", map[string]any{"options": map[string]int{"requestedPolicyVersion": 3}}, &policy); err != nil {
			return notGranted{err}
		}
		var bindings []map[string]any
		if raw, ok := policy["bindings"]; ok {
			if err := json.Unmarshal(raw, &bindings); err != nil {
				return err
			}
		}
		bindings, changed := edit(bindings)
		if !changed {
			return nil
		}
		raw, err := json.Marshal(bindings)
		if err != nil {
			return err
		}
		policy["bindings"] = raw
		policy["version"] = json.RawMessage("3")
		var out map[string]json.RawMessage
		code, err := g.call(ctx, hc, ":setIamPolicy", map[string]any{"policy": policy}, &out)
		// 409: the policy changed since it was read (its etag).
		if code == http.StatusConflict && attempt < 4 {
			continue
		}
		if err != nil && definite(code) {
			return notGranted{err}
		}
		return err
	}
}

func (g *gcpConsole) open(ctx context.Context, p person, until time.Time, grant string) (granted, error) {
	member := "user:" + p.email
	end := until.UTC().Format(time.RFC3339)
	err := g.change(ctx, func(bindings []map[string]any) ([]map[string]any, bool) {
		return append(bindings, map[string]any{
			"role":    g.t.CloudRole,
			"members": []string{member},
			"condition": map[string]string{
				"title":       grant,
				"description": "Grantra console session for " + p.name + " until " + end,
				"expression":  fmt.Sprintf("request.time < timestamp(%q)", end),
			},
		}), true
	})
	if err != nil {
		return granted{}, err
	}
	b, err := json.Marshal(gcpGrant{Title: grant})
	if err != nil {
		return granted{}, err
	}
	return granted{url: g.url(p), account: member, grant: string(b)}, nil
}

func (g *gcpConsole) url(p person) string {
	return "https://console.cloud.google.com/home/dashboard?" + url.Values{"project": {g.t.Address}, "authuser": {p.email}}.Encode()
}

// close removes the session's binding (a condition with its title).
func (g *gcpConsole) close(ctx context.Context, grant string) error {
	var gr gcpGrant
	if json.Unmarshal([]byte(grant), &gr) != nil || gr.Title == "" {
		// Opening failed before saying what it granted: grant is the
		// title it was going to use.
		gr.Title = grant
	}
	return g.change(ctx, func(bindings []map[string]any) ([]map[string]any, bool) {
		kept := bindings[:0]
		for _, b := range bindings {
			if c, ok := b["condition"].(map[string]any); ok && c["title"] == gr.Title {
				continue
			}
			kept = append(kept, b)
		}
		return kept, len(kept) != len(bindings)
	})
}
