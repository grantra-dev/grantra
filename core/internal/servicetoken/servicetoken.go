// Package servicetoken keeps the tokens a company's scripts and scheduled
// jobs use to feed Grantra without a person signing in, such as a nightly
// job that posts the HR export.
//
// A token can only do what its scopes say, each an action alone or pinned
// to one source or application. It always has an expiry date, can be
// revoked at any time, and everything done with it is audited under its own
// name, "service_token:<id>".
package servicetoken

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apikey"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector/flatfile"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Prefix starts every service token, so leaked tokens are easy to spot.
const Prefix = "grantra_st_"

// Scopes a token can hold.
const (
	// ScopeSyncSource feeds an HR source, as POST
	// /api/v1/identity-sources/{id}/runs.
	ScopeSyncSource = "identity_source.sync"
	// ScopeImportAccounts loads an application's account file, as POST
	// /api/v1/applications/{id}/account-imports.
	ScopeImportAccounts = "application.import_accounts"
)

// Scopes lists every scope.
var Scopes = []string{ScopeSyncSource, ScopeImportAccounts}

// Limits on what a token is created with.
const (
	MaxScopes        = 20
	MaxExpiresInDays = 365
)

// Token is a service token, without its secret.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Actor is how the audit log names what the token does.
func (t Token) Actor() string { return "service_token:" + t.ID }

// Allows reports whether the token may do scope on target, the id of the
// source or application.
func (t Token) Allows(scope, target string) bool {
	return slices.Contains(t.Scopes, scope) || slices.Contains(t.Scopes, scope+":"+target)
}

// New is what a token is created with.
type New struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// ExpiresInDays is how long the token works, 1 to 365 days.
	ExpiresInDays int `json:"expires_in_days"`
}

// scopeTargets says, for scopes pinned to one thing, which things exist.
var scopeTargets = map[string]string{
	ScopeSyncSource:     `SELECT EXISTS (SELECT 1 FROM identity_sources WHERE id = $1)`,
	ScopeImportAccounts: `SELECT EXISTS (SELECT 1 FROM applications WHERE id = $1 AND connector = '` + flatfile.Kind + `')`,
}

// Create makes a token. The secret is returned once; Grantra keeps only its
// hash.
func Create(ctx context.Context, tx pgx.Tx, tenantID string, in New, actor string) (Token, string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return Token{}, "", apperr.Invalidf("name is required, up to 100 characters, such as the job using the token")
	}
	if in.ExpiresInDays < 1 || in.ExpiresInDays > MaxExpiresInDays {
		return Token{}, "", apperr.Invalidf("expires_in_days must be between 1 and %d", MaxExpiresInDays)
	}
	if len(in.Scopes) == 0 || len(in.Scopes) > MaxScopes {
		return Token{}, "", apperr.Invalidf("give between 1 and %d scopes, such as %q", MaxScopes, ScopeSyncSource)
	}
	var scopes []string
	for _, s := range in.Scopes {
		s = strings.TrimSpace(s)
		scope, target, pinned := strings.Cut(s, ":")
		if !slices.Contains(Scopes, scope) {
			return Token{}, "", apperr.Invalidf("unknown scope %q (known: %s)", scope, strings.Join(Scopes, ", "))
		}
		if pinned {
			var ok bool
			if db.ValidUUID(target) {
				if err := tx.QueryRow(ctx, scopeTargets[scope], target).Scan(&ok); err != nil {
					return Token{}, "", err
				}
			}
			if !ok {
				return Token{}, "", apperr.Invalidf("scope %q: no such %s", s, targetName(scope))
			}
		}
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}

	secret, shown, err := apikey.New(Prefix)
	if err != nil {
		return Token{}, "", err
	}
	t := Token{Name: in.Name, Prefix: shown, Scopes: scopes, CreatedBy: actor}
	err = tx.QueryRow(ctx, `
		INSERT INTO service_tokens (tenant_id, name, prefix, hash, scopes, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + make_interval(days => $7)) RETURNING id, created_at, expires_at`,
		tenantID, t.Name, t.Prefix, apikey.Hash(secret), t.Scopes, actor, in.ExpiresInDays).Scan(&t.ID, &t.CreatedAt, &t.ExpiresAt)
	if err != nil {
		return Token{}, "", err
	}
	err = db.Audit(ctx, tx, tenantID, actor, "service_token.created", "service_token", t.ID,
		map[string]any{"name": t.Name, "prefix": t.Prefix, "scopes": t.Scopes, "expires_at": t.ExpiresAt})
	return t, secret, err
}

func targetName(scope string) string {
	if scope == ScopeSyncSource {
		return "HR source"
	}
	return "application with the csv connector"
}

const columns = `id, name, prefix, scopes, created_by, created_at, expires_at, last_used_at, revoked_at`

func scan(row pgx.CollectableRow) (Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	return t, err
}

// List returns every token, newest first.
func List(ctx context.Context, tx pgx.Tx) ([]Token, error) {
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM service_tokens ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

// Revoke stops a token working. Revoking a revoked token does nothing.
func Revoke(ctx context.Context, tx pgx.Tx, tenantID, id, actor string) error {
	if !db.ValidUUID(id) {
		return apperr.ErrNotFound
	}
	t, err := db.QueryOne(ctx, tx, scan,
		`UPDATE service_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE id = $1 RETURNING `+columns, id)
	if err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "service_token.revoked", "service_token", id,
		map[string]any{"name": t.Name, "prefix": t.Prefix})
}

// ErrBadToken is a token that is unknown, expired or revoked.
var ErrBadToken = errors.New("invalid, expired or revoked service token")

// Authenticate returns the token behind secret and notes its use, at most
// once a minute.
func Authenticate(ctx context.Context, tx pgx.Tx, secret string) (Token, error) {
	if !strings.HasPrefix(secret, Prefix) {
		return Token{}, ErrBadToken
	}
	t, err := db.QueryOne(ctx, tx, scan, `SELECT `+columns+` FROM service_tokens
		WHERE hash = $1 AND revoked_at IS NULL AND expires_at > now()`, apikey.Hash(secret))
	if errors.Is(err, apperr.ErrNotFound) {
		return Token{}, ErrBadToken
	}
	if err != nil {
		return Token{}, err
	}
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > time.Minute {
		if _, err := tx.Exec(ctx, `UPDATE service_tokens SET last_used_at = now() WHERE id = $1`, t.ID); err != nil {
			return Token{}, fmt.Errorf("note token use: %w", err)
		}
	}
	return t, nil
}
