package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // "0123456789abcdef0123456789abcdef"

func localKey(t *testing.T, encoded string) *LocalKey {
	t.Helper()
	k, err := NewLocalKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestLocalKey(t *testing.T) {
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := NewLocalKey(bad); err == nil {
			t.Errorf("%q was accepted as a master key", bad)
		}
	}
	k := localKey(t, testKey)
	if !strings.HasPrefix(k.KeyID(), "local:") || len(k.KeyID()) != len("local:")+16 {
		t.Fatalf("key id = %q", k.KeyID())
	}
	aad := versionAAD("t", "a", 1)
	s, err := seal(k, []byte("hunter2"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Ciphertext, []byte("hunter2")) || bytes.Contains(s.WrappedKey, []byte("hunter2")) {
		t.Fatal("the secret is readable in what is stored")
	}
	got, err := open(k, s, aad)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}
	// The same value sealed twice looks different: fresh data key and nonce.
	s2, _ := seal(k, []byte("hunter2"), aad)
	if bytes.Equal(s.Ciphertext, s2.Ciphertext) || bytes.Equal(s.WrappedKey, s2.WrappedKey) {
		t.Fatal("sealing is deterministic")
	}

	if _, err := open(k, s, versionAAD("t", "a", 2)); err == nil {
		t.Error("a sealed value opened as another version")
	}
	if _, err := open(k, s, versionAAD("t", "b", 1)); err == nil {
		t.Error("a sealed value opened for another account")
	}
	tampered := s
	tampered.Ciphertext = append([]byte{}, s.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	if _, err := open(k, tampered, aad); err == nil {
		t.Error("a tampered ciphertext opened")
	}
	other := localKey(t, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if _, err := open(other, s, aad); err == nil || !strings.Contains(err.Error(), k.KeyID()) {
		t.Errorf("another master key: err = %v, want it to name the key that wrapped the secret", err)
	}
	// Even claiming the right key id, another key cannot unwrap.
	if _, err := (&LocalKey{id: k.id, aead: other.aead}).Unwrap(k.id, s.WrappedKey, aad); err == nil {
		t.Error("another master key unwrapped the data key")
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != PasswordLength {
			t.Fatalf("%q has %d characters", p, len(p))
		}
		for _, class := range passwordClasses {
			if !strings.ContainsAny(p, class) {
				t.Fatalf("%q has no character of %q", p, class)
			}
		}
		if seen[p] {
			t.Fatalf("%q came out twice", p)
		}
		seen[p] = true
	}
}

// fakeTarget is a connector kind that sets passwords, for rotation tests.
type fakeTarget struct {
	mu        sync.Mutex
	passwords map[string][]string // native id -> every password set
	fail      error
}

func (f *fakeTarget) Read(context.Context) (connector.Snapshot, error) {
	return connector.Snapshot{}, nil
}

func (f *fakeTarget) SetPassword(_ context.Context, id, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.passwords[id] = append(f.passwords[id], password)
	return nil
}

func (f *fakeTarget) last(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.passwords[id]
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

var fakeT = &fakeTarget{passwords: map[string][]string{}}

func init() {
	connector.Register("vault-test", func(json.RawMessage, connector.SecretResolver) (connector.Reader, error) { return fakeT, nil })
	connector.RegisterPasswordSetter("vault-test")
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	v      *Vault
	ctx    context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "vault"), v: New(localKey(t, testKey), connector.EnvSecrets), ctx: context.Background()}
}

func (e *env) tx(fn func(pgx.Tx) error) error {
	return db.InTenant(e.ctx, e.pool, e.tenant, fn)
}

func (e *env) must(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := e.tx(fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) one(sql string, args ...any) (out string) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error { return tx.QueryRow(e.ctx, sql, args...).Scan(&out) })
	return out
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error { _, err := tx.Exec(e.ctx, sql, args...); return err })
}

func (e *env) identity(name, kind string) string {
	return e.one(`INSERT INTO identities (tenant_id, display_name, kind) VALUES ($1, $2, $3) RETURNING id`, e.tenant, name, kind)
}

func (e *env) create(in Input) Account {
	e.t.Helper()
	var a Account
	e.must(func(tx pgx.Tx) (err error) {
		a, err = e.v.Create(e.ctx, tx, e.tenant, "identity:admin", in)
		return err
	})
	return a
}

// grant gives access to a vault entitlement, for a day unless expires says
// otherwise: vault access is just-in-time only.
func (e *env) grant(identity, entitlement string, expires *time.Time) {
	if expires == nil {
		t := time.Now().Add(24 * time.Hour)
		expires = &t
	}
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', $4)`, e.tenant, identity, entitlement, expires)
}

func (e *env) checkout(identity, id string) (Lease, error) {
	var l Lease
	err := e.tx(func(tx pgx.Tx) (err error) {
		l, err = e.v.Checkout(e.ctx, tx, e.tenant, identity, id, "INC-1: restart the replica")
		return err
	})
	return l, err
}

func (e *env) holders(id string) []string {
	e.t.Helper()
	var h []string
	e.must(func(tx pgx.Tx) (err error) { h, err = Holders(e.ctx, tx, id); return err })
	return h
}

// targetAccount adds an application with the rotating test connector and an
// account in it.
func (e *env) targetAccount(native string) string {
	app := e.one(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, $2, 'vault-test') RETURNING id`, e.tenant, "Target "+native)
	return e.one(`INSERT INTO accounts (tenant_id, application_id, native_id, username) VALUES ($1, $2, $3, $3) RETURNING id`, e.tenant, app, native)
}

func isInvalid(err error) bool  { return errors.As(err, new(*apperr.Invalid)) }
func isConflict(err error) bool { return errors.As(err, new(*apperr.Conflict)) }

func TestCreateAndCheckout(t *testing.T) {
	e := newEnv(t)
	ana, luis := e.identity("Ana", "human"), e.identity("Luis", "human")

	a := e.create(Input{Name: "prod-db root", Username: "root", Secret: "s3cret-root", Settings: Settings{Address: "prod-db:5432"}})
	if a.Risk != "critical" || a.EntitlementName != "Use prod-db root" || a.Version != 1 || a.CheckoutMinutes != 60 || a.CanRotate {
		t.Fatalf("account = %+v", a)
	}
	if app := e.one(`SELECT a.name || '/' || a.connector FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1`, a.EntitlementID); app != ApplicationName+"/manual" {
		t.Fatalf("entitlement lives in %s", app)
	}
	// A second account reuses the vault application.
	b := e.create(Input{Name: "dc admin", Username: "Administrator", Risk: "high"})
	if n := e.one(`SELECT count(*)::text FROM applications WHERE name = $1`, ApplicationName); n != "1" {
		t.Fatalf("%s vault applications", n)
	}
	if b.Version != 0 || b.Risk != "high" {
		t.Fatalf("second account = %+v", b)
	}

	// Stored sealed, never in the clear.
	if n := e.one(`SELECT count(*)::text FROM vault_secret_versions WHERE position('s3cret-root'::bytea IN ciphertext) > 0`); n != "0" {
		t.Fatal("the secret is stored in the clear")
	}

	// Nobody holds it yet.
	if h := e.holders(a.ID); len(h) != 0 {
		t.Fatalf("holders = %v", h)
	}
	e.grant(ana, a.EntitlementID, nil)
	past := time.Now().Add(-time.Hour)
	e.grant(luis, a.EntitlementID, &past)
	if h := e.holders(a.ID); len(h) != 1 || h[0] != ana {
		t.Fatalf("holders = %v, want only ana (luis's access expired)", h)
	}
	// Through a business role too.
	role := e.one(`INSERT INTO roles (tenant_id, name) VALUES ($1, 'DBAs') RETURNING id`, e.tenant)
	e.exec(`INSERT INTO role_entitlements (tenant_id, role_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, role, a.EntitlementID)
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '1 day')`, e.tenant, luis, role)
	if h := e.holders(a.ID); len(h) != 2 {
		t.Fatalf("holders = %v, want ana and luis (through the role)", h)
	}
	e.exec(`UPDATE identities SET status = 'suspended' WHERE id = $1`, luis)
	if h := e.holders(a.ID); len(h) != 1 {
		t.Fatalf("holders = %v, want suspended people left out", h)
	}
	for _, missing := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		err := db.InTenant(e.ctx, e.pool, e.tenant, func(tx pgx.Tx) error { _, err := Holders(e.ctx, tx, missing); return err })
		if !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("holders of %s = %v, want not found", missing, err)
		}
	}

	// List: everything for readers, only what you hold for others.
	e.must(func(tx pgx.Tx) error {
		all, err := List(e.ctx, tx, "")
		if err != nil || len(all) != 2 {
			t.Fatalf("all = %d, %v", len(all), err)
		}
		mine, err := List(e.ctx, tx, ana)
		if err != nil || len(mine) != 1 || mine[0].ID != a.ID {
			t.Fatalf("ana's = %+v, %v", mine, err)
		}
		return nil
	})

	l, err := e.checkout(ana, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Secret != "s3cret-root" || l.Version != 1 || l.IdentityName != "Ana" || time.Until(l.ExpiresAt) < 59*time.Minute {
		t.Fatalf("lease = %+v", l)
	}
	// Checking out again extends: one open lease per person.
	if _, err := e.checkout(ana, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT count(*)::text FROM vault_checkouts WHERE vault_account_id = $1 AND ended_at IS NULL`, a.ID); n != "1" {
		t.Fatalf("%s open leases, want 1", n)
	}
	// An account with no secret cannot be checked out.
	if _, err := e.checkout(ana, b.ID); !isConflict(err) {
		t.Fatalf("no secret: err = %v", err)
	}
	// Nor anything with the vault off.
	off := &env{t: t, pool: e.pool, tenant: e.tenant, v: New(nil, nil), ctx: e.ctx}
	if _, err := off.checkout(ana, a.ID); !errors.Is(err, ErrDisabled) {
		t.Fatalf("vault off: err = %v", err)
	}

	// Never in the audit log.
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE details::text LIKE '%s3cret%'`); n != "0" {
		t.Fatal("the secret reached the audit log")
	}
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE action = 'vault.checked_out' AND actor = $1`, db.IdentityActor(ana)); n != "2" {
		t.Fatalf("%s check-outs audited", n)
	}

	// Copying version 1 of one account into another does not open.
	e.exec(`INSERT INTO vault_secret_versions (tenant_id, vault_account_id, version, status, source, key_id, wrapped_key, nonce, ciphertext, created_by)
	        SELECT tenant_id, $2, 1, 'current', source, key_id, wrapped_key, nonce, ciphertext, 'attacker' FROM vault_secret_versions WHERE vault_account_id = $1`, a.ID, b.ID)
	e.grant(ana, b.EntitlementID, nil)
	if _, err := e.checkout(ana, b.ID); err == nil || isConflict(err) {
		t.Fatalf("a secret copied to another account opened: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	for _, in := range []Input{
		{Name: "", Username: "root"},
		{Name: "x", Username: ""},
		{Name: "x", Username: "root", Kind: "token"},
		{Name: "x", Username: "root", Risk: "low"},
		{Name: "x", Username: "root", Settings: Settings{CheckoutMinutes: 2}},
		{Name: "x", Username: "root", Settings: Settings{CheckoutMinutes: 600}},
		{Name: "x", Username: "root", Settings: Settings{RotateEveryDays: 30}},      // not linked to a rotating account
		{Name: "x", Username: "root", Settings: Settings{RotateAfterCheckin: true}}, // same
		{Name: "x", Username: "root", AccountID: "nope"},
		{Name: "x", Username: "root", Secret: strings.Repeat("a", maxSecret+1)},
	} {
		err := e.tx(func(tx pgx.Tx) error { _, err := e.v.Create(e.ctx, tx, e.tenant, "identity:admin", in); return err })
		if !isInvalid(err) {
			t.Errorf("%+v: err = %v, want invalid", in, err)
		}
	}
	acct := e.targetAccount("svc-backup")
	if a := e.create(Input{Name: "ssh", Kind: KindSSHKey, AccountID: acct}); a.Username != "svc-backup" {
		t.Fatalf("username = %q, want the account's", a.Username)
	}
	err := e.tx(func(tx pgx.Tx) error {
		_, err := e.v.Create(e.ctx, tx, e.tenant, "identity:admin", Input{Name: "ssh2", Kind: KindSSHKey, AccountID: e.targetAccount("other"), Settings: Settings{RotateEveryDays: 7}})
		return err
	})
	if !isInvalid(err) {
		t.Fatalf("rotating an SSH key: err = %v", err)
	}
	err = e.tx(func(tx pgx.Tx) error {
		_, err := e.v.Create(e.ctx, tx, e.tenant, "identity:admin", Input{Name: "ssh", Username: "root"})
		return err
	})
	if !isConflict(err) {
		t.Fatalf("duplicate name: err = %v", err)
	}
	err = e.tx(func(tx pgx.Tx) error {
		_, err := e.v.Create(e.ctx, tx, e.tenant, "identity:admin", Input{Name: "again", AccountID: acct})
		return err
	})
	if !isConflict(err) {
		t.Fatalf("same account twice: err = %v", err)
	}
	// Turned off, the vault still manages accounts but takes no secrets.
	off := New(nil, nil)
	err = e.tx(func(tx pgx.Tx) error {
		_, err := off.Create(e.ctx, tx, e.tenant, "identity:admin", Input{Name: "y", Username: "root", Secret: "x"})
		return err
	})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("secret with the vault off: err = %v", err)
	}
	// Someone else's application already called "Privileged vault".
	e2 := newEnv(t)
	e2.exec(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, $2, 'scim')`, e2.tenant, ApplicationName)
	err = e2.tx(func(tx pgx.Tx) error {
		_, err := e2.v.Create(e2.ctx, tx, e2.tenant, "identity:admin", Input{Name: "z", Username: "root"})
		return err
	})
	if !isConflict(err) {
		t.Fatalf("name taken by a connected application: err = %v", err)
	}
}

func TestExclusiveCheckin(t *testing.T) {
	e := newEnv(t)
	ana, luis := e.identity("Ana", "human"), e.identity("Luis", "human")
	a := e.create(Input{Name: "break-glass", Username: "bg", Secret: "pw", Settings: Settings{Exclusive: true, CheckoutMinutes: 5}})
	e.grant(ana, a.EntitlementID, nil)
	e.grant(luis, a.EntitlementID, nil)

	l, err := e.checkout(ana, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkout(luis, a.ID); !isConflict(err) || !strings.Contains(err.Error(), "Ana") {
		t.Fatalf("luis while ana has it: err = %v", err)
	}
	e.must(func(tx pgx.Tx) error { return Checkin(e.ctx, tx, e.tenant, db.IdentityActor(ana), l.ID, "checked_in") })
	if err := e.tx(func(tx pgx.Tx) error { return Checkin(e.ctx, tx, e.tenant, db.IdentityActor(ana), l.ID, "checked_in") }); !isConflict(err) {
		t.Fatalf("second check-in: err = %v", err)
	}
	l2, err := e.checkout(luis, a.ID)
	if err != nil {
		t.Fatalf("luis after ana checked in: %v", err)
	}

	// The clock ends leases.
	e.exec(`UPDATE vault_checkouts SET expires_at = now() - interval '1 second' WHERE id = $1`, l2.ID)
	var n int
	e.must(func(tx pgx.Tx) (err error) { n, err = ExpireCheckouts(e.ctx, tx, e.tenant); return err })
	if n != 1 || e.one(`SELECT ended_by FROM vault_checkouts WHERE id = $1`, l2.ID) != "expired" {
		t.Fatalf("expired %d", n)
	}
	if _, err := e.checkout(ana, a.ID); err != nil {
		t.Fatalf("after luis's lease expired: %v", err)
	}
}

func rotationState(e *env, id string) (failures int, rotateAt *time.Time, lastErr string) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT rotation_failures, rotate_at, coalesce(last_rotation_error, '') FROM vault_accounts WHERE id = $1`, id).
			Scan(&failures, &rotateAt, &lastErr)
	})
	return
}

func TestRotation(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "human")
	acct := e.targetAccount("svc-db")
	a := e.create(Input{Name: "db svc", AccountID: acct, Secret: "old-pw", Settings: Settings{RotateEveryDays: 30, RotateAfterCheckin: true}})
	if !a.CanRotate || a.RotateAt == nil || time.Until(*a.RotateAt) < 29*24*time.Hour {
		t.Fatalf("account = %+v, want rotation in 30 days", a)
	}
	e.grant(ana, a.EntitlementID, nil)

	// Rotate now: the target and the vault get the same new password.
	r, err := e.v.Rotate(e.ctx, e.pool, e.tenant, "identity:admin", a.ID)
	if err != nil || !r.Rotated || r.Version != 2 {
		t.Fatalf("rotate = %+v, %v", r, err)
	}
	l, err := e.checkout(ana, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Secret == "old-pw" || l.Secret != fakeT.last("svc-db") || len(l.Secret) != PasswordLength {
		t.Fatalf("vault has %q, target has %q", l.Secret, fakeT.last("svc-db"))
	}
	if s := e.one(`SELECT string_agg(version || ':' || status || ':' || source, ',' ORDER BY version) FROM vault_secret_versions WHERE vault_account_id = $1`, a.ID); s != "1:retired:set,2:current:rotation" {
		t.Fatalf("versions = %s", s)
	}

	// Checked out, it does not rotate on schedule; checked in, it does.
	e.exec(`UPDATE vault_accounts SET rotate_at = now() - interval '1 minute' WHERE id = $1`, a.ID)
	_, out, err := e.v.RotateDue(e.ctx, e.pool, e.tenant)
	if err != nil || len(out) != 0 {
		t.Fatalf("rotated while checked out: %+v, %v", out, err)
	}
	e.exec(`UPDATE vault_accounts SET rotate_at = NULL WHERE id = $1`, a.ID)
	e.must(func(tx pgx.Tx) error { return Checkin(e.ctx, tx, e.tenant, db.IdentityActor(ana), l.ID, "checked_in") })
	if _, at, _ := rotationState(e, a.ID); at == nil || time.Until(*at) > 0 {
		t.Fatalf("rotate_at = %v after check-in, want now", at)
	}

	// The target refuses: the pending password is kept and retried later.
	fakeT.fail = errors.New("password does not meet the policy")
	_, out, err = e.v.RotateDue(e.ctx, e.pool, e.tenant)
	if err != nil || len(out) != 1 || out[0].Rotated || !strings.Contains(out[0].Error, "policy") {
		t.Fatalf("failing rotation = %+v, %v", out, err)
	}
	failures, at, lastErr := rotationState(e, a.ID)
	if failures != 1 || at == nil || time.Until(*at) < 4*time.Minute || !strings.Contains(lastErr, "policy") {
		t.Fatalf("after a failure: %d failures, retry at %v, error %q", failures, at, lastErr)
	}
	pending := e.one(`SELECT version::text FROM vault_secret_versions WHERE vault_account_id = $1 AND status = 'pending'`, a.ID)
	if pending != "3" {
		t.Fatalf("pending version = %s", pending)
	}
	if l, _ := e.checkout(ana, a.ID); l.Version != 2 {
		t.Fatalf("checked out version %d while rotation failed, want the current one", l.Version)
	}
	e.exec(`UPDATE vault_checkouts SET ended_at = now(), ended_by = 'checked_in' WHERE vault_account_id = $1`, a.ID)

	// The retry sends the same password: the target may already have it.
	fakeT.fail = nil
	var pendingPassword string
	e.must(func(tx pgx.Tx) (err error) {
		pendingPassword, err = e.v.openVersion(e.ctx, tx, e.tenant, a.ID, 3)
		return err
	})
	e.exec(`UPDATE vault_accounts SET rotate_at = now() WHERE id = $1`, a.ID)
	_, out, err = e.v.RotateDue(e.ctx, e.pool, e.tenant)
	if err != nil || len(out) != 1 || !out[0].Rotated || out[0].Version != 3 {
		t.Fatalf("retry = %+v, %v", out, err)
	}
	if fakeT.last("svc-db") != pendingPassword {
		t.Fatal("the retry sent a new password instead of the pending one")
	}
	failures, at, lastErr = rotationState(e, a.ID)
	if failures != 0 || lastErr != "" || at == nil || time.Until(*at) < 29*24*time.Hour {
		t.Fatalf("after success: %d failures, next %v, error %q", failures, at, lastErr)
	}
	audits := e.one(`SELECT string_agg(action, ',' ORDER BY id) FROM audit_events WHERE target_id = $1 AND action LIKE 'vault.rot%'`, a.ID)
	if audits != "vault.rotated,vault.rotation_failed,vault.rotated" {
		t.Fatalf("audit = %s", audits)
	}
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE details::text LIKE '%' || $1 || '%'`, pendingPassword); n != "0" {
		t.Fatal("a generated password reached the audit log")
	}

	// An admin typing a secret abandons a rotation in flight.
	fakeT.fail = errors.New("down")
	if _, err := e.v.Rotate(e.ctx, e.pool, e.tenant, "identity:admin", a.ID); err != nil {
		t.Fatal(err)
	}
	fakeT.fail = nil
	e.must(func(tx pgx.Tx) error { return e.v.SetSecret(e.ctx, tx, e.tenant, "identity:admin", a.ID, "typed-pw") })
	if s := e.one(`SELECT string_agg(version || ':' || status, ',' ORDER BY version) FROM vault_secret_versions WHERE vault_account_id = $1 AND version > 3`, a.ID); s != "4:failed,5:current" {
		t.Fatalf("versions = %s", s)
	}
	if f, _, _ := rotationState(e, a.ID); f != 0 {
		t.Fatalf("failures = %d after a typed secret", f)
	}

	// Accounts that cannot rotate say so.
	plain := e.create(Input{Name: "plain", Username: "root", Secret: "x"})
	if _, err := e.v.Rotate(e.ctx, e.pool, e.tenant, "identity:admin", plain.ID); !isInvalid(err) {
		t.Fatalf("unlinked: err = %v", err)
	}
	// One whose link broke stops retrying on schedule.
	e.exec(`UPDATE vault_accounts SET rotate_at = now() WHERE id = $1`, a.ID)
	e.exec(`UPDATE applications SET connector = 'manual' WHERE id = (SELECT application_id FROM accounts WHERE id = $1)`, acct)
	_, out, err = e.v.RotateDue(e.ctx, e.pool, e.tenant)
	if err != nil || len(out) != 1 || out[0].Error == "" {
		t.Fatalf("broken link = %+v, %v", out, err)
	}
	if _, at, lastErr := rotationState(e, a.ID); at != nil || lastErr == "" {
		t.Fatalf("broken link: next %v, error %q; want no more tries and the reason kept", at, lastErr)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "human")
	acct := e.targetAccount("svc-app")
	a := e.create(Input{Name: "app svc", AccountID: acct, Secret: "pw"})
	if a.RotateAt != nil {
		t.Fatalf("rotate_at = %v without a schedule", a.RotateAt)
	}
	e.must(func(tx pgx.Tx) (err error) {
		a, err = e.v.Update(e.ctx, tx, e.tenant, "identity:admin", a.ID, Settings{RotateEveryDays: 7, CheckoutMinutes: 15, Notes: " on-call only "})
		return err
	})
	if a.RotateAt == nil || time.Until(*a.RotateAt) < 6*24*time.Hour || a.CheckoutMinutes != 15 || a.Notes != "on-call only" {
		t.Fatalf("updated = %+v", a)
	}
	e.must(func(tx pgx.Tx) (err error) {
		a, err = e.v.Update(e.ctx, tx, e.tenant, "identity:admin", a.ID, Settings{})
		return err
	})
	if a.RotateAt != nil || a.CheckoutMinutes != 60 {
		t.Fatalf("schedule off = %+v", a)
	}
	if err := e.tx(func(tx pgx.Tx) error {
		_, err := e.v.Update(e.ctx, tx, e.tenant, "identity:admin", "00000000-0000-0000-0000-000000000000", Settings{})
		return err
	}); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing: err = %v", err)
	}

	e.grant(ana, a.EntitlementID, nil)
	if _, err := e.checkout(ana, a.ID); err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", a.ID) })
	for _, q := range []string{
		`SELECT count(*)::text FROM vault_accounts WHERE id = $1`,
		`SELECT count(*)::text FROM vault_secret_versions WHERE vault_account_id = $1`,
		`SELECT count(*)::text FROM vault_checkouts WHERE vault_account_id = $1`,
	} {
		if n := e.one(q, a.ID); n != "0" {
			t.Fatalf("%s: %s left", q, n)
		}
	}
	if n := e.one(`SELECT count(*)::text FROM access_assignments WHERE entitlement_id = $1`, a.EntitlementID); n != "0" {
		t.Fatalf("%s assignments of the deleted account left", n)
	}
	if n := e.one(`SELECT count(*)::text FROM entitlements WHERE id = $1`, a.EntitlementID); n != "0" {
		t.Fatal("the entitlement outlived its vault account")
	}
}

func TestDiscover(t *testing.T) {
	e := newEnv(t)
	person, svc := e.identity("Ana", "human"), e.identity("Backup job", "service")
	app := e.one(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'AD', 'vault-test') RETURNING id`, e.tenant)
	var admins, users string
	e.must(func(tx pgx.Tx) error {
		en, err := catalog.CreateEntitlement(e.ctx, tx, e.tenant, app, catalog.EntitlementInput{Name: "Domain Admins", Risk: "critical"})
		admins = en.ID
		if err != nil {
			return err
		}
		en, err = catalog.CreateEntitlement(e.ctx, tx, e.tenant, app, catalog.EntitlementInput{Name: "Users"})
		users = en.ID
		return err
	})
	account := func(native, identity, status string, ents ...string) string {
		id := e.one(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username, status) VALUES ($1, $2, $3, $4, $4, $5) RETURNING id`,
			e.tenant, app, db.Nullable(identity), native, status)
		for _, en := range ents {
			e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, id, en)
		}
		return id
	}
	anaAdmin := account("ana-admin", person, "active", admins)
	account("ana", person, "active", users)               // plain user: not a finding
	orphan := account("old-svc", "", "active", users)     // nobody's
	backup := account("svc-backup", svc, "active", users) // a service's
	account("gone-admin", "", "disabled", admins)         // disabled: skipped
	// Accounts of the built-in application are platform roles, not targets.
	e.exec(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Grantra', 'grantra')`, e.tenant)

	e.create(Input{Name: "backup", AccountID: backup})

	var got []Finding
	e.must(func(tx pgx.Tx) (err error) { got, err = Discover(e.ctx, tx, 50); return err })
	byAccount := map[string]Finding{}
	for _, f := range got {
		byAccount[f.AccountID] = f
	}
	if len(got) != 3 {
		t.Fatalf("findings = %+v", got)
	}
	if f := byAccount[anaAdmin]; strings.Join(f.Reasons, ",") != "privileged" || strings.Join(f.Entitlements, ",") != "Domain Admins" || !f.CanRotate || f.IdentityName != "Ana" {
		t.Errorf("ana-admin = %+v", f)
	}
	if f := byAccount[orphan]; strings.Join(f.Reasons, ",") != "orphan" || len(f.Entitlements) != 0 {
		t.Errorf("orphan = %+v", f)
	}
	if f := byAccount[backup]; strings.Join(f.Reasons, ",") != "service" || f.VaultAccountID == "" {
		t.Errorf("backup = %+v", f)
	}
	// What is still outside the vault comes first, privileged first.
	if got[0].AccountID != anaAdmin || got[2].AccountID != backup {
		t.Errorf("order = %s, %s, %s", got[0].Username, got[1].Username, got[2].Username)
	}
}

func TestNoBirthrightVaultAccess(t *testing.T) {
	e := newEnv(t)
	a := e.create(Input{Name: "prod-db root", Username: "root"})
	plain := e.one(`INSERT INTO roles (tenant_id, name) VALUES ($1, 'Staff') RETURNING id`, e.tenant)
	var dbas catalog.Role
	e.must(func(tx pgx.Tx) (err error) {
		// A role may bundle vault access: people request the role.
		dbas, err = catalog.CreateRole(e.ctx, tx, e.tenant, catalog.RoleInput{Name: "DBAs", EntitlementIDs: []string{a.EntitlementID}})
		return err
	})
	for name, in := range map[string]access.RuleInput{
		"the entitlement":      {Name: "everyone", EntitlementIDs: []string{a.EntitlementID}},
		"a role that gives it": {Name: "everyone", RoleIDs: []string{plain, dbas.ID}},
	} {
		err := e.tx(func(tx pgx.Tx) error { _, err := access.CreateRule(e.ctx, tx, e.tenant, in); return err })
		if !isInvalid(err) {
			t.Errorf("rule giving %s: err = %v, want invalid", name, err)
		}
	}
	// A role a rule already gives cannot take vault access in.
	e.must(func(tx pgx.Tx) error {
		_, err := access.CreateRule(e.ctx, tx, e.tenant, access.RuleInput{Name: "staff", RoleIDs: []string{plain}})
		return err
	})
	err := e.tx(func(tx pgx.Tx) error {
		_, err := catalog.UpdateRole(e.ctx, tx, e.tenant, plain, catalog.RoleInput{Name: "Staff", EntitlementIDs: []string{a.EntitlementID}})
		return err
	})
	if !isInvalid(err) {
		t.Fatalf("vault access into a birthright role: err = %v", err)
	}
	e.must(func(tx pgx.Tx) error {
		_, err := catalog.UpdateRole(e.ctx, tx, e.tenant, dbas.ID, catalog.RoleInput{Name: "DBA team", EntitlementIDs: []string{a.EntitlementID}})
		return err
	})
}
