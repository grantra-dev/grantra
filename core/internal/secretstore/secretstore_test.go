package secretstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func key(t *testing.T, encoded string) *vault.LocalKey {
	t.Helper()
	k, err := vault.NewLocalKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ring wraps with cur and unwraps with whichever key wrapped, like a vault
// moving from one key to another.
type ring struct{ cur, old *vault.LocalKey }

func (r ring) KeyID() string                            { return r.cur.KeyID() }
func (r ring) Wrap(dataKey, aad []byte) ([]byte, error) { return r.cur.Wrap(dataKey, aad) }
func (r ring) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if keyID == r.old.KeyID() {
		return r.old.Unwrap(keyID, wrapped, aad)
	}
	return r.cur.Unwrap(keyID, wrapped, aad)
}

func (s *Store) create(t *testing.T, label, value string) (string, error) {
	t.Helper()
	var ref string
	err := db.InTenant(context.Background(), s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		ref, err = s.Create(context.Background(), tx, "identity:test", label, value)
		return err
	})
	return ref, err
}

func TestStore(t *testing.T) {
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "secrets")
	ctx := context.Background()

	// Without a vault key nothing typed in the console can be kept, and env:
	// references still work.
	off := &Store{Pool: pool, TenantID: tenant}
	if _, err := off.create(t, "Okta token", "00abc"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("create without a key: %v", err)
	}
	t.Setenv("GRANTRA_SECRET_TEST_STORE", "from-env")
	if v, err := off.Resolve("env:GRANTRA_SECRET_TEST_STORE"); err != nil || v != "from-env" {
		t.Fatalf("env reference = %q, %v", v, err)
	}

	k1 := key(t, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	s := &Store{Pool: pool, TenantID: tenant, Keys: k1}
	for _, bad := range [][2]string{{"", "x"}, {"label", "  "}, {"label", strings.Repeat("x", MaxValue+1)}} {
		var inv *apperr.Invalid
		if _, err := s.create(t, bad[0], bad[1]); !errors.As(err, &inv) {
			t.Errorf("create %q: %v", bad[0], err)
		}
	}
	ref, err := s.create(t, "Okta token", "00abc-s3cret")
	if err != nil || !strings.HasPrefix(ref, Prefix) {
		t.Fatalf("create = %q, %v", ref, err)
	}
	if v, err := s.Resolve(ref); err != nil || v != "00abc-s3cret" {
		t.Fatalf("resolve = %q, %v", v, err)
	}
	var stored []byte
	err = db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ciphertext FROM connector_secrets WHERE id = $1`, strings.TrimPrefix(ref, Prefix)).Scan(&stored)
	})
	if err != nil || strings.Contains(string(stored), "s3cret") {
		t.Fatalf("stored in the clear: %v", err)
	}
	if n := countAudit(t, s, "connector_secret.created"); n != 1 {
		t.Errorf("%d audit events", n)
	}

	// Another tenant cannot use the reference, and unknown ones fail.
	other := &Store{Pool: pool, TenantID: dbtest.Tenant(t, pool, "secrets"), Keys: k1}
	for _, r := range []string{ref, "secret:00000000-0000-0000-0000-000000000000", "secret:../x"} {
		if v, err := other.Resolve(r); err == nil {
			t.Errorf("%s resolved to %q in another tenant", r, v)
		}
	}
	if _, err := off.Resolve(ref); err == nil || !strings.Contains(err.Error(), vault.KeyEnv) {
		t.Errorf("resolve without the key: %v", err)
	}

	// Moving to a new key: rewrap, then the old key is no longer needed.
	k2 := key(t, "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
	var n int
	err = db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) (err error) {
		n, err = Rewrap(ctx, tx, tenant, ring{cur: k2, old: k1})
		return err
	})
	if err != nil || n != 1 {
		t.Fatalf("rewrap = %d, %v", n, err)
	}
	moved := &Store{Pool: pool, TenantID: tenant, Keys: k2}
	if v, err := moved.Resolve(ref); err != nil || v != "00abc-s3cret" {
		t.Fatalf("resolve after rewrap = %q, %v", v, err)
	}
	if _, err := s.Resolve(ref); err == nil {
		t.Error("the old key still opens the secret")
	}
}

func countAudit(t *testing.T, s *Store, action string) int {
	t.Helper()
	var n int
	err := db.InTenant(context.Background(), s.Pool, s.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
