package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

const sshTestKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF3uQ6N98lHfI+IpQ+pIyl3fXYAg4N9OpL16ZPxPtrvD ana@laptop"

func TestSSHAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")

	create := `{"name": "prod-db", "login": "postgres", "address": "10.0.0.5"}`
	if rec := e.do(auditor, http.MethodPost, "/api/v1/ssh/targets", create); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/ssh/targets", create)
	tg := decode[sshaccess.Target](t, rec)
	if rec.Code != http.StatusCreated || tg.Address != "10.0.0.5:22" {
		t.Fatalf("create = %d %+v", rec.Code, tg)
	}
	certs := "/api/v1/ssh/targets/" + tg.ID + "/certificates"
	body := `{"public_key": "` + sshTestKey + `"}`

	// With the vault key off, nothing is signed.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '2 hours')`, e.tenant, ana, tg.EntitlementID)
	if rec := e.do(ana, http.MethodPost, certs, body); rec.Code != http.StatusConflict {
		t.Errorf("issue with SSH off = %d %s", rec.Code, rec.Body)
	}
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.SSH = sshaccess.New(key)
	e.api.GatewayAddress = "ssh.example.com:2222"

	// The CA public keys are public: servers fetch them without signing in.
	cas := map[string]string{}
	for _, file := range []string{"ca.pub", "gateway_ca.pub"} {
		pub := httptest.NewRecorder()
		e.mux.ServeHTTP(pub, httptest.NewRequest(http.MethodGet, "/api/v1/ssh/"+file, nil))
		if pub.Code != http.StatusOK || !strings.HasPrefix(pub.Body.String(), "ssh-ed25519 ") || pub.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s = %d %q", file, pub.Code, pub.Body)
		}
		cas[file] = strings.TrimSpace(pub.Body.String())
	}
	if cas["ca.pub"] == cas["gateway_ca.pub"] {
		t.Error("people and the gateway share a CA")
	}

	// Only holders get certificates: not an admin, not an auditor.
	for _, who := range []string{admin, auditor, luis} {
		if rec := e.do(who, http.MethodPost, certs, body); rec.Code != http.StatusForbidden {
			t.Errorf("certificate without access = %d", rec.Code)
		}
	}
	rec = e.do(ana, http.MethodPost, certs, body)
	cert := decode[sshaccess.Certificate](t, rec)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(cert.Certificate, "ssh-ed25519-cert-v01@openssh.com ") || cert.Principal != "grantra:prod-db" {
		t.Fatalf("issue = %d %+v", rec.Code, cert)
	}

	// Ana sees her target, Luis none, the auditor all.
	list := func(as string) []sshaccess.Target {
		t.Helper()
		rec := e.do(as, http.MethodGet, "/api/v1/ssh/targets", "")
		return decode[struct {
			Targets []sshaccess.Target `json:"targets"`
		}](t, rec).Targets
	}
	if got := list(ana); len(got) != 1 || !got[0].Held {
		t.Errorf("ana's targets = %+v", got)
	}
	if got := list(luis); len(got) != 0 {
		t.Errorf("luis's targets = %+v", got)
	}
	if got := list(auditor); len(got) != 1 || got[0].Held {
		t.Errorf("auditor's targets = %+v", got)
	}
	info := decode[map[string]any](t, e.do(ana, http.MethodGet, "/api/v1/ssh", ""))
	if info["ca_public_key"] != cas["ca.pub"] || info["gateway_ca_public_key"] != cas["gateway_ca.pub"] ||
		info["gateway_address"] != "ssh.example.com:2222" {
		t.Errorf("ssh info = %+v", info)
	}

	// Sessions, recordings and commands are for vault readers.
	var session string
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) (err error) {
		session, err = sshaccess.StartSession(context.Background(), tx, e.tenant, sshaccess.Session{TargetID: tg.ID, TargetName: tg.Name,
			Login: tg.Login, IdentityID: ana, ClientAddress: "1.2.3.4:5", Gateway: "gw", Kind: "shell", Width: 80, Height: 24})
		if err != nil {
			return err
		}
		if err := sshaccess.AppendRecording(context.Background(), tx, e.tenant, session, 0, []byte(`{"version":2}`+"\n"), false); err != nil {
			return err
		}
		return sshaccess.AddCommands(context.Background(), tx, e.tenant, session, []sshaccess.Command{{Command: "DROP TABLE orders"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/ssh/sessions", "/api/v1/ssh/sessions/" + session, "/api/v1/ssh/sessions/" + session + "/recording",
		"/api/v1/ssh/commands?q=drop"} {
		if rec := e.do(ana, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("ana reads %s = %d", path, rec.Code)
		}
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("auditor reads %s = %d %s", path, rec.Code, rec.Body)
		}
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/ssh/sessions/"+session+"/recording", "")
	if rec.Header().Get("Content-Type") != "application/x-asciicast" || rec.Body.String() != `{"version":2}`+"\n" {
		t.Errorf("recording = %q %s", rec.Header().Get("Content-Type"), rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'ssh.recording_viewed' AND actor = 'identity:' || $1`, auditor); n != 2 {
		t.Errorf("recording views audited = %d", n)
	}
	found := decode[struct {
		Commands []sshaccess.Command `json:"commands"`
	}](t, e.do(auditor, http.MethodGet, "/api/v1/ssh/commands?q=drop+table", ""))
	if len(found.Commands) != 1 || found.Commands[0].IdentityName != "ana" {
		t.Errorf("search = %+v", found)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/ssh/commands?q=", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty search = %d", rec.Code)
	}

	terminate := "/api/v1/ssh/sessions/" + session + "/terminate"
	if rec := e.do(auditor, http.MethodPost, terminate, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor terminates = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, terminate, ""); rec.Code != http.StatusNoContent {
		t.Errorf("admin terminates = %d %s", rec.Code, rec.Body)
	}

	rec = e.do(admin, http.MethodPut, "/api/v1/ssh/targets/"+tg.ID, `{"max_certificate_hours": 2, "via_gateway": false}`)
	if got := decode[sshaccess.Target](t, rec); rec.Code != http.StatusOK || got.ViaGateway || got.MaxCertificateHours != 2 {
		t.Errorf("update = %d %+v", rec.Code, got)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/ssh/targets/"+tg.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	if got := list(auditor); len(got) != 0 {
		t.Errorf("targets after delete = %+v", got)
	}
}
