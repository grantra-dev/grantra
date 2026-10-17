package appauthz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apikey"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// KeyPrefix starts every API key, so leaked keys are easy to spot.
const KeyPrefix = "grantra_ak_"

// Key is an application's API key, without the secret.
type Key struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CreateKey gives an application a new API key. The secret is returned
// once; Grantra keeps only its hash.
func CreateKey(ctx context.Context, tx pgx.Tx, tenantID, appID, name, actor string) (Key, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Key{}, "", apperr.Invalidf("name is required, up to 100 characters, such as the app or environment using the key")
	}
	if err := appExists(ctx, tx, appID); err != nil {
		return Key{}, "", err
	}
	secret, shown, err := apikey.New(KeyPrefix)
	if err != nil {
		return Key{}, "", err
	}
	k := Key{Name: name, Prefix: shown, CreatedBy: actor}
	err = tx.QueryRow(ctx, `
		INSERT INTO app_api_keys (tenant_id, application_id, name, prefix, hash, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		tenantID, appID, k.Name, k.Prefix, apikey.Hash(secret), actor).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return Key{}, "", err
	}
	err = db.Audit(ctx, tx, tenantID, actor, "app_authz.key_created", "application", appID,
		map[string]any{"key_id": k.ID, "name": k.Name, "prefix": k.Prefix})
	return k, secret, err
}

// ListKeys lists an application's keys, newest first.
func ListKeys(ctx context.Context, tx pgx.Tx, appID string) ([]Key, error) {
	if err := appExists(ctx, tx, appID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, name, prefix, created_by, created_at, last_used_at, revoked_at
		FROM app_api_keys WHERE application_id = $1 ORDER BY created_at DESC, id`, appID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) {
		var k Key
		err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
		return k, err
	})
}

// RevokeKey stops a key working. Revoking a revoked key does nothing.
func RevokeKey(ctx context.Context, tx pgx.Tx, tenantID, appID, keyID, actor string) error {
	if !db.ValidUUID(appID) || !db.ValidUUID(keyID) {
		return apperr.ErrNotFound
	}
	tag, err := tx.Exec(ctx, `
		UPDATE app_api_keys SET revoked_at = coalesce(revoked_at, now())
		WHERE application_id = $1 AND id = $2`, appID, keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return db.Audit(ctx, tx, tenantID, actor, "app_authz.key_revoked", "application", appID, map[string]any{"key_id": keyID})
}

// ErrBadKey is a key that is unknown or revoked.
var ErrBadKey = errors.New("invalid or revoked API key")

// AppForKey returns the application a key belongs to, and notes its use, at
// most once a minute so busy apps do not write on every question.
func AppForKey(ctx context.Context, tx pgx.Tx, secret string) (string, error) {
	if !strings.HasPrefix(secret, KeyPrefix) {
		return "", ErrBadKey
	}
	var (
		app  string
		used *time.Time
	)
	err := tx.QueryRow(ctx, `SELECT application_id, last_used_at FROM app_api_keys WHERE hash = $1 AND revoked_at IS NULL`,
		apikey.Hash(secret)).Scan(&app, &used)
	if err == nil && (used == nil || time.Since(*used) > time.Minute) {
		_, err = tx.Exec(ctx, `UPDATE app_api_keys SET last_used_at = now() WHERE hash = $1`, apikey.Hash(secret))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrBadKey
	}
	return app, err
}
