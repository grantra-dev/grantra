package gateways

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func TestDesktopAndCloudTargets(t *testing.T) {
	e := newEnv(t)
	acct := e.vaultAccount("desktop admin", vault.KindPassword, "pw")
	sub := "/subscriptions/0b1f6471-1bf0-4dda-aec3-111122223333"
	role := "arn:aws:iam::123456789012:role/ReadOnly"

	rdp, err := e.create(Input{Name: "win-1", Kind: RDP, Settings: Settings{Address: ptr("10.0.0.9"), VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	if rdp.Address != "10.0.0.9:3389" || rdp.TLSMode != TLSVerify || rdp.Clipboard != ClipboardPaste || rdp.RDPSecurity != SecurityNLA || rdp.MaxTokenHours != 8 ||
		rdp.EntitlementName != "Use the win-1 desktop" {
		t.Errorf("desktop = %+v", rdp)
	}
	aws, err := e.create(Input{Name: "prod-aws", Kind: AWS, Settings: Settings{Address: &role, VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	// AWS roles last an hour unless the role allows more.
	if aws.MaxTokenHours != 1 || aws.EntitlementName != "Open the prod-aws AWS console" {
		t.Errorf("aws = %+v", aws)
	}
	az, err := e.create(Input{Name: "prod-azure", Kind: Azure, Settings: Settings{Address: &sub, CloudRole: ptr("Reader")}})
	if err != nil {
		t.Fatal(err)
	}
	gcp, err := e.create(Input{Name: "prod-gcp", Kind: GCP, Settings: Settings{Address: ptr("shop-prod-123"),
		CloudRole: ptr("roles/compute.viewer"), VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	if az.CloudRole != "Reader" || gcp.CloudRole != "roles/compute.viewer" {
		t.Errorf("roles = %q %q", az.CloudRole, gcp.CloudRole)
	}
	if app := e.one(`SELECT a.name FROM applications a JOIN entitlements e ON e.application_id = a.id WHERE e.id = $1`, gcp.EntitlementID); app != "Remote access" {
		t.Errorf("application = %s", app)
	}

	for _, c := range []struct {
		name string
		in   Input
		msg  string
	}{
		{"rdp without account", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h")}}, "logs in to the desktop"},
		{"rdp without tls", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, TLSMode: ptr(TLSDisable)}}, "always reached over TLS"},
		{"rdp with a CA", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, CACertificate: ptr(testCA(t))}}, "ca_certificate is for"},
		{"rdp bad clipboard", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, Clipboard: ptr("copy")}}, "clipboard must be"},
		{"rdp host with comma", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("a,b"), VaultAccountID: &acct}}, "host name or IP"},
		{"rdp legacy security", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, RDPSecurity: ptr("any")}}, "rdp_security must be nla or tls"},
		{"rdp security on a database", Input{Name: "x", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, RDPSecurity: ptr("tls")}}, "rdp_security is for desktops"},
		{"clipboard on a database", Input{Name: "x", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, Clipboard: ptr("both")}}, "clipboard is for desktops"},
		{"database on a desktop", Input{Name: "x", Kind: RDP, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, Database: ptr("d")}}, "database is for"},
		{"aws bad arn", Input{Name: "x", Kind: AWS, Settings: Settings{Address: ptr("arn:aws:iam::1:user/bob"), VaultAccountID: &acct}}, "ARN of the IAM role"},
		{"aws without account", Input{Name: "x", Kind: AWS, Settings: Settings{Address: &role}}, "access key"},
		{"aws too long", Input{Name: "x", Kind: AWS, Settings: Settings{Address: &role, VaultAccountID: &acct, MaxTokenHours: ptr(13)}}, "between 1 and 12"},
		{"aws not verified", Input{Name: "x", Kind: AWS, Settings: Settings{Address: &role, VaultAccountID: &acct, TLSMode: ptr(TLSRequire)}}, "must be verify"},
		{"aws with a role", Input{Name: "x", Kind: AWS, Settings: Settings{Address: &role, VaultAccountID: &acct, CloudRole: ptr("Reader")}}, "cloud_role is for"},
		{"gcp basic role", Input{Name: "x", Kind: GCP, Settings: Settings{Address: ptr("shop-prod-123"), CloudRole: ptr("roles/owner")}}, "cannot make roles/owner expire"},
		{"gcp no role", Input{Name: "x", Kind: GCP, Settings: Settings{Address: ptr("shop-prod-123")}}, "cloud_role must be"},
		{"gcp bad project", Input{Name: "x", Kind: GCP, Settings: Settings{Address: ptr("Shop"), CloudRole: ptr("roles/compute.viewer")}}, "project ID"},
		{"azure bad scope", Input{Name: "x", Kind: Azure, Settings: Settings{Address: ptr("/subscriptions/x"), CloudRole: ptr("Reader")}}, "scope"},
		{"azure app without directory", Input{Name: "x", Kind: Azure, Settings: Settings{Address: &sub, CloudRole: ptr("Reader"), VaultAccountID: &acct}}, "cloud_directory must name"},
		{"azure bad directory", Input{Name: "x", Kind: Azure, Settings: Settings{Address: &sub, CloudRole: ptr("Reader"), CloudDirectory: ptr("not a domain")}}, "cloud_directory must be"},
		{"directory on gcp", Input{Name: "x", Kind: GCP, Settings: Settings{Address: ptr("shop-prod-123"), CloudRole: ptr("roles/compute.viewer"), CloudDirectory: ptr("example.com")}}, "cloud_directory is for Azure"},
		{"unknown kind", Input{Name: "x", Kind: "vnc"}, "kind must be one of"},
	} {
		if _, err := e.create(c.in); !isInvalid(err) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	// Settings change, and stay checked.
	var up Target
	e.must(func(tx pgx.Tx) (err error) {
		up, err = Update(e.ctx, tx, e.tenant, "identity:admin", rdp.ID, Settings{Clipboard: ptr(ClipboardNone), TLSMode: ptr(TLSRequire), RDPSecurity: ptr(SecurityTLS)})
		return err
	})
	if up.Clipboard != ClipboardNone || up.TLSMode != TLSRequire || up.RDPSecurity != SecurityTLS || up.Address != rdp.Address {
		t.Errorf("updated = %+v", up)
	}
	err = e.tx(func(tx pgx.Tx) error {
		_, err := Update(e.ctx, tx, e.tenant, "identity:admin", gcp.ID, Settings{CloudRole: ptr("roles/editor")})
		return err
	})
	if !isInvalid(err) {
		t.Errorf("gcp to a basic role: %v", err)
	}

	// A console with sessions running stays until they end: what they
	// granted is taken back with its settings.
	ana := e.identity("ana")
	e.grant(ana, gcp.EntitlementID, "2 hours")
	var tok Token
	var session string
	e.must(func(tx pgx.Tx) (err error) {
		if tok, err = Issue(e.ctx, tx, e.tenant, ana, gcp.ID); err != nil {
			return err
		}
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: gcp.ID, TargetName: gcp.Name, Kind: GCP, IdentityID: ana,
			TokenID: tok.ID, ClientAddress: "1.2.3.4:5", Gateway: "console", CloudGrant: "grantra-x"}, "")
		return err
	})
	err = e.tx(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", gcp.ID) })
	var cf *apperr.Conflict
	if !errors.As(err, &cf) || !strings.Contains(err.Error(), "1 console sessions running") {
		t.Errorf("delete with a session = %v", err)
	}
	// Nor does it move: the grant is taken back from the project it is in.
	err = e.tx(func(tx pgx.Tx) error {
		_, err := Update(e.ctx, tx, e.tenant, "identity:admin", gcp.ID, Settings{Address: ptr("shop-other-123")})
		return err
	})
	if !errors.As(err, &cf) {
		t.Errorf("move with a session = %v", err)
	}
	// Its notes and hours can still change.
	err = e.tx(func(tx pgx.Tx) error {
		_, err := Update(e.ctx, tx, e.tenant, "identity:admin", gcp.ID, Settings{Notes: ptr("still here")})
		return err
	})
	if err != nil {
		t.Errorf("notes with a session = %v", err)
	}
	// Nor are console sessions lost: the gateway does not touch them.
	e.exec(`UPDATE gateway_sessions SET last_seen_at = now() - interval '1 hour' WHERE id = $1`, session)
	e.must(func(tx pgx.Tx) error {
		n, err := EndLostSessions(e.ctx, tx, e.tenant)
		if n != 0 {
			t.Errorf("%d lost", n)
		}
		return err
	})
	var s Session
	e.must(func(tx pgx.Tx) (err error) { s, err = GetSession(e.ctx, tx, session); return err })
	if s.EndedAt != nil || s.CloudGrant != "grantra-x" || s.ValidBefore == nil || !s.ValidBefore.Equal(tok.ValidBefore) {
		t.Errorf("session = %+v", s)
	}
	e.must(func(tx pgx.Tx) error { return EndSession(e.ctx, tx, e.tenant, session, EndClosed, "") })
	e.must(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", gcp.ID) })

	// AWS cannot cut a console session short; others can.
	e.grant(ana, aws.EntitlementID, "2 hours")
	e.must(func(tx pgx.Tx) (err error) {
		if tok, err = Issue(e.ctx, tx, e.tenant, ana, aws.ID); err != nil {
			return err
		}
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: aws.ID, TargetName: aws.Name, Kind: AWS, IdentityID: ana,
			TokenID: tok.ID, ClientAddress: "1.2.3.4:5", Gateway: "console"}, acct)
		return err
	})
	err = e.tx(func(tx pgx.Tx) error { return Terminate(e.ctx, tx, e.tenant, "identity:admin", session) })
	if !errors.As(err, &cf) || !strings.Contains(err.Error(), "revoke the role's active sessions") {
		t.Errorf("terminate on AWS = %v", err)
	}
}

func TestRecording(t *testing.T) {
	e := newEnv(t)
	acct := e.vaultAccount("desktop admin", vault.KindPassword, "pw")
	rdp, err := e.create(Input{Name: "win-1", Kind: RDP, Settings: Settings{Address: ptr("10.0.0.9"), VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	ana := e.identity("ana")
	var session string
	e.must(func(tx pgx.Tx) (err error) {
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: rdp.ID, TargetName: rdp.Name, Kind: RDP, IdentityID: ana,
			ClientAddress: "1.2.3.4:5", Gateway: "gw"}, acct)
		if err != nil {
			return err
		}
		for i, part := range []string{"4.sync,1.1;", "", "4.sync,1.2;"} {
			if err := AppendRecording(e.ctx, tx, e.tenant, session, i, []byte(part)); err != nil {
				return err
			}
		}
		return nil
	})
	var out bytes.Buffer
	started := false
	e.must(func(tx pgx.Tx) error { return RecordingViewed(e.ctx, tx, e.tenant, "identity:auditor", session) })
	e.must(func(tx pgx.Tx) error {
		return WriteRecording(e.ctx, tx, session, func() { started = true }, &out)
	})
	if out.String() != "4.sync,1.1;4.sync,1.2;" || !started {
		t.Errorf("recording = %q", out.String())
	}
	if n := e.one(`SELECT recording_bytes::text FROM gateway_sessions WHERE id = $1`, session); n != "22" {
		t.Errorf("recording bytes = %s", n)
	}
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE action = 'gateway.recording_viewed' AND target_id = $1`, session); n != "1" {
		t.Errorf("%s views audited", n)
	}
	// Database sessions have a log, not a recording.
	pg, err := e.create(Input{Name: "db", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) (err error) {
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: pg.ID, TargetName: pg.Name, Kind: Postgres, IdentityID: ana,
			ClientAddress: "1.2.3.4:5", Gateway: "gw"}, acct)
		return err
	})
	err = e.tx(func(tx pgx.Tx) error {
		return RecordingViewed(e.ctx, tx, e.tenant, "identity:auditor", session)
	})
	if !isInvalid(err) {
		t.Errorf("database recording = %v", err)
	}
}
