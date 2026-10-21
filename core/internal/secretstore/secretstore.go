// Package secretstore keeps the connector secrets admins type in the
// console (API tokens, client secrets, service account keys), sealed with
// the vault key like the vault's own secrets. A connector config refers to
// one as "secret:<id>"; nobody can read it back, only replace it.
//
// The Store is also the SecretResolver every connector gets: "secret:"
// references are read from the database, and "env:" references from the
// server's environment, as before.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// Prefix starts a reference to a stored secret.
const Prefix = "secret:"

// MaxValue is the longest secret Grantra stores; a service account key is a
// few kilobytes.
const MaxValue = 16 << 10

// ErrNoKey is returned when there is no vault key to seal secrets with.
var ErrNoKey error = &apperr.Conflict{Msg: "Grantra has no vault key, so it cannot keep secrets typed in the console: set " +
	vault.KeyEnv + " (or a KMS), or give a reference to an environment variable such as env:" + connector.SecretEnvPrefix + "CRM_TOKEN"}

// Store keeps one tenant's connector secrets.
type Store struct {
	Pool     *pgxpool.Pool
	TenantID string
	// Keys seals and opens secrets; without it only env: references work.
	Keys vault.KeyProvider
}

func aad(tenantID, id string) []byte {
	return fmt.Appendf(nil, "grantra-connector-secret:v1:%s:%s", tenantID, id)
}

// Create seals value and returns the reference to put in a connector
// config. label says what the secret is for, as the audit log shows it.
func (s *Store) Create(ctx context.Context, tx pgx.Tx, actor, label, value string) (string, error) {
	if s.Keys == nil {
		return "", ErrNoKey
	}
	label = strings.TrimSpace(label)
	if label == "" || utf8.RuneCountInString(label) > 200 {
		return "", apperr.Invalidf("label must be between 1 and 200 characters")
	}
	if strings.TrimSpace(value) == "" || len(value) > MaxValue {
		return "", apperr.Invalidf("the secret must be between 1 and %d bytes", MaxValue)
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}
	sealed, err := vault.Seal(s.Keys, []byte(value), aad(s.TenantID, id))
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO connector_secrets (id, tenant_id, label, key_id, wrapped_key, nonce, ciphertext, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, s.TenantID, label, sealed.KeyID, sealed.WrappedKey, sealed.Nonce, sealed.Ciphertext, actor)
	if err != nil {
		return "", err
	}
	err = db.Audit(ctx, tx, s.TenantID, actor, "connector_secret.created", "connector_secret", id, map[string]any{"label": label})
	return Prefix + id, err
}

// resolveTimeout bounds reading a secret, which runs outside the caller's
// transaction.
const resolveTimeout = 10 * time.Second

// Resolve turns a reference into its value: "secret:<id>" from the
// database, anything else through connector.EnvSecrets.
func (s *Store) Resolve(ref string) (string, error) {
	id, ok := strings.CutPrefix(ref, Prefix)
	if !ok {
		return connector.EnvSecrets(ref)
	}
	if !db.ValidUUID(id) {
		return "", fmt.Errorf("secret %q does not exist", ref)
	}
	if s.Keys == nil {
		return "", errors.New("the secret was typed in the console, but Grantra has no vault key to open it: set " + vault.KeyEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	var sealed vault.Sealed
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT key_id, wrapped_key, nonce, ciphertext FROM connector_secrets WHERE id = $1`, id).
			Scan(&sealed.KeyID, &sealed.WrappedKey, &sealed.Nonce, &sealed.Ciphertext)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("secret %q does not exist", ref)
	}
	if err != nil {
		return "", err
	}
	v, err := vault.Open(s.Keys, sealed, aad(s.TenantID, id))
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", ref, err)
	}
	return string(v), nil
}

// Rewrap re-wraps the data key of every connector secret that the current
// key did not wrap, for grantra vault rewrap. It returns how many.
func Rewrap(ctx context.Context, tx pgx.Tx, tenantID string, kp vault.KeyProvider) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id, key_id, wrapped_key FROM connector_secrets WHERE key_id <> $1 FOR UPDATE`, kp.KeyID())
	if err != nil {
		return 0, err
	}
	type secret struct {
		id, keyID string
		wrapped   []byte
	}
	todo, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (secret, error) {
		var s secret
		return s, row.Scan(&s.id, &s.keyID, &s.wrapped)
	})
	if err != nil {
		return 0, err
	}
	for _, s := range todo {
		wrapped, err := vault.RewrapKey(kp, s.keyID, s.wrapped, aad(tenantID, s.id))
		if err != nil {
			return 0, fmt.Errorf("connector secret %s: %w", s.id, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE connector_secrets SET key_id = $2, wrapped_key = $3 WHERE id = $1`, s.id, kp.KeyID(), wrapped); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}
