package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func TestGatewayAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Vault = vault.New(key, nil)
	rec := e.do(admin, http.MethodPost, "/api/v1/vault/accounts", `{"name": "orders owner", "username": "orders", "secret": "pw"}`)
	acct := decode[vault.Account](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("vault account = %d %s", rec.Code, rec.Body)
	}

	create := `{"name": "orders-db", "kind": "postgres", "address": "10.0.0.7", "database": "orders", "vault_account_id": "` + acct.ID + `"}`
	if rec := e.do(auditor, http.MethodPost, "/api/v1/gateway/targets", create); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/gateway/targets", `{"name": "orders-db", "kind": "postgres", "address": "10.0.0.7"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("database without a vault account = %d %s", rec.Code, rec.Body)
	}
	rec = e.do(admin, http.MethodPost, "/api/v1/gateway/targets", create)
	tg := decode[gateways.Target](t, rec)
	if rec.Code != http.StatusCreated || tg.Address != "10.0.0.7:5432" || tg.TLSMode != gateways.TLSVerify {
		t.Fatalf("create = %d %+v", rec.Code, tg)
	}
	path := "/api/v1/gateway/targets/" + tg.ID
	if rec := e.do(admin, http.MethodPut, path, `{"max_token_hours": 4}`); rec.Code != http.StatusOK ||
		decode[gateways.Target](t, rec).MaxTokenHours != 4 {
		t.Errorf("update = %d", rec.Code)
	}
	tokens := path + "/tokens"

	// With the vault key off, the gateway is off.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '2 hours')`, e.tenant, ana, tg.EntitlementID)
	if rec := e.do(ana, http.MethodPost, tokens, ""); rec.Code != http.StatusConflict {
		t.Errorf("token with the gateway off = %d %s", rec.Code, rec.Body)
	}
	ca := httptest.NewRecorder()
	e.mux.ServeHTTP(ca, httptest.NewRequest(http.MethodGet, "/api/v1/gateway/ca.pem", nil))
	if ca.Code != http.StatusNotFound {
		t.Errorf("CA with the gateway off = %d", ca.Code)
	}
	e.api.Gateways = gateways.New(key)
	e.api.GatewayAddresses = GatewayAddresses{Kubernetes: "gw.example.com:8443", Postgres: "gw.example.com:15432"}

	// The CA is public: clients fetch it without signing in.
	ca = httptest.NewRecorder()
	e.mux.ServeHTTP(ca, httptest.NewRequest(http.MethodGet, "/api/v1/gateway/ca.pem", nil))
	if ca.Code != http.StatusOK || !strings.HasPrefix(ca.Body.String(), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("CA = %d %s", ca.Code, ca.Body)
	}
	info := decode[map[string]any](t, e.do(ana, http.MethodGet, "/api/v1/gateway", ""))
	if info["enabled"] != true || info["postgres_address"] != "gw.example.com:15432" || info["ca_certificate"] != ca.Body.String() {
		t.Errorf("gateway info = %+v", info)
	}

	// Only holders get tokens: not an admin, not an auditor.
	for _, who := range []string{admin, auditor, luis} {
		if rec := e.do(who, http.MethodPost, tokens, ""); rec.Code != http.StatusForbidden {
			t.Errorf("token without access = %d", rec.Code)
		}
	}
	rec = e.do(ana, http.MethodPost, tokens, "")
	tok := decode[struct {
		gateways.Token
		Kubeconfig    string `json:"kubeconfig"`
		CACertificate string `json:"ca_certificate"`
	}](t, rec)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(tok.Token.Token, gateways.TokenPrefix) || tok.Kubeconfig != "" ||
		tok.CACertificate != ca.Body.String() || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token = %d %s", rec.Code, rec.Body)
	}

	// Ana sees her target without what the gateway logs in with; Luis sees
	// none; the auditor all.
	list := func(as string) []gateways.Target {
		t.Helper()
		return decode[struct {
			Targets []gateways.Target `json:"targets"`
		}](t, e.do(as, http.MethodGet, "/api/v1/gateway/targets", "")).Targets
	}
	if got := list(ana); len(got) != 1 || !got[0].Held || got[0].VaultAccountID != "" {
		t.Errorf("ana's targets = %+v", got)
	}
	if got := list(luis); len(got) != 0 {
		t.Errorf("luis's targets = %+v", got)
	}
	if got := list(auditor); len(got) != 1 || got[0].Held || got[0].VaultAccountID != acct.ID {
		t.Errorf("auditor's targets = %+v", got)
	}

	// A cluster's token comes with its kubeconfig.
	rec = e.do(admin, http.MethodPost, "/api/v1/gateway/targets", `{"name": "prod", "kind": "kubernetes", "address": "https://10.0.0.1:6443"}`)
	cluster := decode[gateways.Target](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create cluster = %d %s", rec.Code, rec.Body)
	}
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '2 hours')`, e.tenant, ana, cluster.EntitlementID)
	rec = e.do(ana, http.MethodPost, "/api/v1/gateway/targets/"+cluster.ID+"/tokens", "")
	if body := rec.Body.String(); rec.Code != http.StatusCreated || !strings.Contains(body, "https://gw.example.com:8443/kubernetes/prod") {
		t.Errorf("cluster token = %d %s", rec.Code, body)
	}

	// Sessions and their logs are for vault readers.
	var session string
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) (err error) {
		session, err = gateways.StartSession(context.Background(), tx, e.tenant, gateways.Session{TargetID: tg.ID, TargetName: tg.Name,
			Kind: tg.Kind, IdentityID: ana, TokenID: tok.ID, ClientAddress: "1.2.3.4:5", Gateway: "gw", Account: "orders"}, acct.ID)
		if err != nil {
			return err
		}
		return gateways.AddEvents(context.Background(), tx, e.tenant, session, []gateways.Event{{Kind: gateways.EventQuery, Text: "DELETE FROM orders"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/v1/gateway/sessions", "/api/v1/gateway/sessions/" + session, "/api/v1/gateway/events?q=delete"} {
		if rec := e.do(ana, http.MethodGet, p, ""); rec.Code != http.StatusForbidden {
			t.Errorf("ana reads %s = %d", p, rec.Code)
		}
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/gateway/sessions/"+session, "")
	got := decode[struct {
		gateways.Session
		Log []gateways.Event `json:"log"`
	}](t, rec)
	if rec.Code != http.StatusOK || len(got.Log) != 1 || got.Log[0].Text != "DELETE FROM orders" {
		t.Errorf("session = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/gateway/sessions/"+session+"?after="+itoa(got.Log[0].ID), ""); rec.Code != http.StatusOK ||
		len(decode[struct {
			Log []gateways.Event `json:"log"`
		}](t, rec).Log) != 0 {
		t.Errorf("after the last event = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/gateway/sessions/"+session+"?after=x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad after = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'gateway.session_viewed' AND target_id = $1`, session); n != 1 {
		t.Errorf("%d views audited", n)
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/gateway/events?q=delete", "")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "DELETE FROM orders") {
		t.Errorf("search = %d %s", rec.Code, body)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/gateway/sessions?active=true", ""); !strings.Contains(rec.Body.String(), session) {
		t.Errorf("active sessions = %s", rec.Body)
	}

	// Admins end sessions; auditors do not.
	end := "/api/v1/gateway/sessions/" + session + "/terminate"
	if rec := e.do(auditor, http.MethodPost, end, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor ends = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, end, ""); rec.Code != http.StatusNoContent {
		t.Errorf("admin ends = %d %s", rec.Code, rec.Body)
	}

	// A target goes with its access.
	if rec := e.do(auditor, http.MethodDelete, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor deletes = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, path, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	if got := list(auditor); len(got) != 1 || got[0].Name != "prod" {
		t.Errorf("after delete = %+v", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
