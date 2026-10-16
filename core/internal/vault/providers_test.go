package vault

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/vault/kms"
)

// fakeKMS is an external key: a local key under another key id.
type fakeKMS struct{ k *LocalKey }

func newFakeKMS(t *testing.T) *fakeKMS {
	return &fakeKMS{localKey(t, "ZmFrZS1rbXMta2V5LWZha2Uta21zLWtleS0xMjM0NTY=")}
}

func (f *fakeKMS) KeyID() string                            { return "fake:key" }
func (f *fakeKMS) Owns(keyID string) bool                   { return strings.HasPrefix(keyID, "fake:") }
func (f *fakeKMS) Wrap(dataKey, aad []byte) ([]byte, error) { return f.k.Wrap(dataKey, aad) }
func (f *fakeKMS) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if !f.Owns(keyID) {
		return nil, errors.New("not a fake key")
	}
	return f.k.Unwrap(f.k.KeyID(), wrapped, aad)
}

var _ kms.Provider = (*fakeKMS)(nil)

func (e *env) secretOf(kp KeyProvider, id string) (string, string) {
	e.t.Helper()
	var s sealed
	var version int
	e.must(func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT version, key_id, wrapped_key, nonce, ciphertext FROM vault_secret_versions WHERE vault_account_id = $1 ORDER BY version DESC LIMIT 1`, id).
			Scan(&version, &s.KeyID, &s.WrappedKey, &s.Nonce, &s.Ciphertext)
	})
	pt, err := open(kp, s, versionAAD(e.tenant, id, version))
	if err != nil {
		return s.KeyID, "error: " + err.Error()
	}
	return s.KeyID, string(pt)
}

func (e *env) rewrap(kp KeyProvider) int {
	e.t.Helper()
	var n int
	e.must(func(tx pgx.Tx) (err error) { n, err = Rewrap(e.ctx, tx, e.tenant, kp); return err })
	return n
}

// TestMoveToKMS moves a vault from GRANTRA_VAULT_KEY to an external key: the
// old key keeps opening what it wrapped until grantra vault rewrap, after
// which the vault works without it.
func TestMoveToKMS(t *testing.T) {
	e := newEnv(t)
	local := e.v.keys
	a := e.create(Input{Name: "prod-db root", Username: "root", Secret: "s3cret-root"})

	ext := newFakeKMS(t)
	ring := &keyring{current: ext, others: []kms.Provider{local.(*LocalKey)}}
	if keyID, got := e.secretOf(ring, a.ID); keyID != local.KeyID() || got != "s3cret-root" {
		t.Fatalf("before rewrap: %s %q", keyID, got)
	}
	// New secrets go to the external key.
	e.v = New(ring, e.v.secrets)
	b := e.create(Input{Name: "dc admin", Username: "Administrator", Secret: "dc-pass"})
	if keyID, got := e.secretOf(ring, b.ID); keyID != "fake:key" || got != "dc-pass" {
		t.Fatalf("new secret: %s %q", keyID, got)
	}
	// Without the old key, the old secret does not open yet.
	if _, got := e.secretOf(&keyring{current: ext}, a.ID); !strings.Contains(got, "not configured to use") {
		t.Fatalf("old secret without old key: %q", got)
	}

	if n := e.rewrap(ring); n != 1 {
		t.Fatalf("rewrapped %d, want 1 (only the old secret)", n)
	}
	if n := e.rewrap(ring); n != 0 {
		t.Fatalf("second rewrap did %d", n)
	}
	for id, want := range map[string]string{a.ID: "s3cret-root", b.ID: "dc-pass"} {
		if keyID, got := e.secretOf(&keyring{current: ext}, id); keyID != "fake:key" || got != want {
			t.Fatalf("after rewrap: %s %q", keyID, got)
		}
	}

	// A rewrap that cannot unwrap changes nothing.
	e.exec(`UPDATE vault_secret_versions SET key_id = 'other:key' WHERE vault_account_id = $1`, a.ID)
	err := e.tx(func(tx pgx.Tx) error { _, err := Rewrap(e.ctx, tx, e.tenant, ring); return err })
	if err == nil || !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("rewrap of unknown key: %v", err)
	}
}

func TestKeysFromEnv(t *testing.T) {
	for _, v := range []string{KeyEnv, ProviderEnv, KMSKeyEnv, OldProviderEnv, OldKMSKeyEnv} {
		t.Setenv(v, "")
	}
	ctx := context.Background()
	if kp, err := KeysFromEnv(ctx); err != nil || kp != nil {
		t.Fatalf("nothing set: %v %v", kp, err)
	}
	t.Setenv(ProviderEnv, "local")
	if _, err := KeysFromEnv(ctx); err == nil {
		t.Fatal("local without a key accepted")
	}
	t.Setenv(KeyEnv, testKey)
	if kp, err := KeysFromEnv(ctx); err != nil || kp.KeyID() != localKey(t, testKey).KeyID() {
		t.Fatalf("local: %v", err)
	}
	t.Setenv(ProviderEnv, "transit")
	if _, err := KeysFromEnv(ctx); err == nil || !strings.Contains(err.Error(), KMSKeyEnv) {
		t.Fatalf("transit without a key: %v", err)
	}
	t.Setenv(ProviderEnv, "rot13")
	if _, err := KeysFromEnv(ctx); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("unknown provider: %v", err)
	}
	t.Setenv(KMSKeyEnv, "grantra")
	t.Setenv(ProviderEnv, "pkcs11")
	t.Setenv(OldProviderEnv, "rot13")
	if _, err := KeysFromEnv(ctx); err == nil || !strings.Contains(err.Error(), OldProviderEnv) {
		t.Fatalf("unknown old provider: %v", err)
	}
	t.Setenv(OldProviderEnv, "")
	if _, err := KeysFromEnv(ctx); err == nil {
		t.Fatal("pkcs11 without a module accepted")
	}
}
