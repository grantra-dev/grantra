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
	"github.com/grantra-dev/grantra/core/internal/notify"
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
	// ScopeSendSignIns sends sign-ins from another identity provider for
	// threat detection, as POST /api/v1/sign-in-events. It cannot be
	// pinned to anything.
	ScopeSendSignIns = "sign_in_event.send"
)

// Scopes lists every scope.
var Scopes = []string{ScopeSyncSource, ScopeImportAccounts, ScopeSendSignIns}

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
	// CreatorName is the display name of the person who created the token,
	// and CreatorLeft whether they have since left the company.
	CreatorName string `json:"creator_name,omitempty"`
	CreatorLeft bool   `json:"creator_left,omitempty"`
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
			if _, ok := scopeTargets[scope]; !ok {
				return Token{}, "", apperr.Invalidf("scope %q cannot be limited to one thing; use %q", s, scope)
			}
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
	toks, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, err
	}
	return toks, addCreators(ctx, tx, toks)
}

// addCreators fills in who created each token and whether they left.
func addCreators(ctx context.Context, tx pgx.Tx, toks []Token) error {
	var ids []string
	for _, t := range toks {
		if id, ok := strings.CutPrefix(t.CreatedBy, db.IdentityActor("")); ok && db.ValidUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	type creator struct {
		name string
		left bool
	}
	creators := map[string]creator{}
	rows, err := tx.Query(ctx, `SELECT id, display_name, status = 'terminated' FROM identities WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	var (
		id string
		c  creator
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &c.name, &c.left}, func() error {
		creators[db.IdentityActor(id)] = c
		return nil
	})
	if err != nil {
		return err
	}
	for i := range toks {
		if c, ok := creators[toks[i].CreatedBy]; ok {
			toks[i].CreatorName, toks[i].CreatorLeft = c.name, c.left
		}
	}
	return nil
}

// CreatorsLeft tells the admins about the tokens still working that the
// people in identityIDs created, when actor (the HR sync or the end-date
// check) makes them leavers. The tokens keep working, because the jobs using
// them belong to the company (and the HR sync that handles leavers may be one
// of them), but an admin who stays should decide whether to keep, replace or
// revoke each one.
func CreatorsLeft(ctx context.Context, tx pgx.Tx, tenantID, actor string, identityIDs []string) error {
	if len(identityIDs) == 0 {
		return nil
	}
	actors := make([]string, len(identityIDs))
	for i, id := range identityIDs {
		actors[i] = db.IdentityActor(id)
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM service_tokens
		WHERE created_by = ANY($1) AND revoked_at IS NULL AND expires_at > now() ORDER BY created_at, id`, actors)
	if err != nil {
		return err
	}
	toks, err := pgx.CollectRows(rows, scan)
	if err != nil || len(toks) == 0 {
		return err
	}
	if err := addCreators(ctx, tx, toks); err != nil {
		return err
	}
	list := make([]map[string]any, len(toks))
	for i, t := range toks {
		list[i] = map[string]any{"name": t.Name, "prefix": t.Prefix, "creator": t.CreatorName}
		err := db.Audit(ctx, tx, tenantID, actor, "service_token.creator_left", "service_token", t.ID,
			map[string]any{"name": t.Name, "prefix": t.Prefix, "created_by": t.CreatedBy})
		if err != nil {
			return err
		}
	}
	admins, err := notify.Admins(ctx, tx)
	if err != nil {
		return err
	}
	return notify.Enqueue(ctx, tx, tenantID, notify.TokenCreatorLeft, admins,
		map[string]any{"tokens": list, "count": len(list)})
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
