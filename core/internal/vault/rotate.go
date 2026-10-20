package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/settings"
	"github.com/grantra-dev/grantra/core/internal/textutil"
)

// Rotation changes a vault account's password in the target system:
//
//  1. In one transaction, a new random password is sealed and saved as a
//     pending version. If a pending version is already there (a rotation
//     that crashed or failed), its password is used again instead, because
//     the target may already have it.
//  2. The connector sets the password in the target.
//  3. In a second transaction, the pending version becomes current, or the
//     failure is recorded and the rotation retried later with backoff.
//
// So a password the target accepted is always in the vault, even if Grantra
// stops between steps.

// rotateTimeout bounds how long a target may take to set a password.
const rotateTimeout = time.Minute

// dueBatch is how many accounts one pass rotates at most.
const dueBatch = 25

// Rotation is the outcome of one rotation.
type Rotation struct {
	VaultAccountID string `json:"vault_account_id"`
	Version        int    `json:"version"`
	Rotated        bool   `json:"rotated"`
	Error          string `json:"error,omitempty"`
}

type target struct {
	native, kind string
	config       json.RawMessage
}

// begin locks an account, checks that it can rotate, and returns its
// pending version and password, creating them when there is none.
func (v *Vault) begin(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, skipLocked bool) (int, string, target, error) {
	var t target
	var kind string
	q := `SELECT va.kind, coalesce(a.native_id, ''), coalesce(app.connector, ''), coalesce(app.config, '{}')
		  FROM vault_accounts va
		  LEFT JOIN accounts a ON a.id = va.account_id
		  LEFT JOIN applications app ON app.id = a.application_id
		  WHERE va.id = $1 FOR UPDATE OF va`
	if skipLocked {
		q += ` SKIP LOCKED`
	}
	err := tx.QueryRow(ctx, q, id).Scan(&kind, &t.native, &t.kind, &t.config)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", t, apperr.ErrNotFound
	}
	if err != nil {
		return 0, "", t, err
	}
	switch {
	case kind != KindPassword:
		return 0, "", t, apperr.Invalidf("only passwords rotate")
	case t.native == "" || !connector.SetsPasswords(t.kind):
		return 0, "", t, apperr.Invalidf("rotation needs the vault account linked to an account in an application whose connector can set passwords")
	}
	var version int
	err = tx.QueryRow(ctx,
		`SELECT version FROM vault_secret_versions WHERE vault_account_id = $1 AND status = 'pending'`, id).Scan(&version)
	if err == nil {
		password, err := v.openVersion(ctx, tx, tenantID, id, version)
		return version, password, t, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", t, err
	}
	length, err := settings.Get(ctx, tx, settings.PasswordLength)
	if err != nil {
		return 0, "", t, err
	}
	password, err := GeneratePassword(length)
	if err != nil {
		return 0, "", t, err
	}
	version, err = v.insertVersion(ctx, tx, tenantID, actor, id, password, "rotation", "pending")
	return version, password, t, err
}

// Rotate changes a vault account's password now, whatever its schedule and
// whoever has it checked out. actor is who asked for it.
func (v *Vault) Rotate(ctx context.Context, pool *pgxpool.Pool, tenantID, actor, id string) (Rotation, error) {
	return v.rotate(ctx, pool, tenantID, actor, id, "manual", false)
}

func (v *Vault) rotate(ctx context.Context, pool *pgxpool.Pool, tenantID, actor, id, trigger string, skipLocked bool) (Rotation, error) {
	r := Rotation{VaultAccountID: id}
	if !v.Enabled() {
		return r, ErrDisabled
	}
	if !db.ValidUUID(id) {
		return r, apperr.ErrNotFound
	}
	var (
		password string
		t        target
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		r.Version, password, t, err = v.begin(ctx, tx, tenantID, actor, id, skipLocked)
		return err
	})
	if err != nil {
		return r, err
	}

	setErr := v.setPassword(ctx, t, password)

	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, _, _, err := lock(ctx, tx, id); err != nil {
			return err
		}
		if setErr != nil {
			r.Error = setErr.Error()
			if _, err := tx.Exec(ctx,
				`UPDATE vault_accounts SET rotation_failures = rotation_failures + 1, last_rotation_error = $2, updated_at = now(),
				        rotate_at = now() + least(interval '5 minutes' * power(2, rotation_failures), interval '1 day')
				 WHERE id = $1`, id, textutil.Clip(r.Error, 1000)); err != nil {
				return err
			}
			return db.Audit(ctx, tx, tenantID, actor, "vault.rotation_failed", "vault_account", id,
				map[string]any{"version": r.Version, "trigger": trigger, "error": textutil.Clip(r.Error, 1000)})
		}
		if _, err := tx.Exec(ctx,
			`UPDATE vault_secret_versions SET status = 'retired' WHERE vault_account_id = $1 AND status = 'current'`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE vault_secret_versions SET status = 'current' WHERE vault_account_id = $1 AND version = $2 AND status = 'pending'`, id, r.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// An admin typed a new secret meanwhile; theirs wins, and the
			// target now holds ours, so say so.
			r.Error = "an admin set the secret while this rotation ran; rotate again so the target and the vault agree"
			_, err := tx.Exec(ctx, `UPDATE vault_accounts SET rotate_at = now(), last_rotation_error = $2 WHERE id = $1`, id, r.Error)
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE vault_accounts SET rotation_failures = 0, last_rotation_error = NULL, updated_at = now(),
			        rotate_at = now() + make_interval(days => rotate_every_days)
			 WHERE id = $1`, id); err != nil {
			return err
		}
		r.Rotated = true
		return db.Audit(ctx, tx, tenantID, actor, "vault.rotated", "vault_account", id,
			map[string]any{"version": r.Version, "trigger": trigger})
	})
	return r, err
}

func (v *Vault) setPassword(ctx context.Context, t target, password string) error {
	c, err := connector.New(t.kind, t.config, v.secrets)
	if err != nil {
		return err
	}
	ps, ok := c.(connector.PasswordSetter)
	if !ok {
		return fmt.Errorf("the %s connector cannot set passwords", t.kind)
	}
	ctx, cancel := context.WithTimeout(ctx, rotateTimeout)
	defer cancel()
	return ps.SetPassword(ctx, t.native, password)
}

// RotateDue ends the check-outs whose time ran out, then rotates the
// accounts whose rotation is due and that nobody has checked out.
func (v *Vault) RotateDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (expired int, out []Rotation, err error) {
	var due []string
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if expired, err = ExpireCheckouts(ctx, tx, tenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT va.id FROM vault_accounts va
			 WHERE va.rotate_at <= now() AND va.kind = 'password' AND va.account_id IS NOT NULL
			   AND NOT EXISTS (SELECT 1 FROM vault_checkouts c WHERE c.vault_account_id = va.id AND c.ended_at IS NULL)
			 ORDER BY va.rotate_at LIMIT $1`, dueBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil || !v.Enabled() {
		return expired, nil, err
	}
	for _, id := range due {
		r, err := v.rotate(ctx, pool, tenantID, "system:vault", id, "schedule", true)
		switch {
		case errors.Is(err, apperr.ErrNotFound):
			continue // deleted, or another replica has it
		case err != nil && ctx.Err() != nil:
			return expired, out, ctx.Err()
		case errors.As(err, new(*apperr.Invalid)):
			// The account can no longer rotate (its link or connector
			// changed): stop retrying until someone fixes it.
			r.Error = err.Error()
			if err := stopRotation(ctx, pool, tenantID, id, r.Error); err != nil {
				return expired, out, err
			}
		case err != nil:
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return expired, out, nil
}

// stopRotation takes an account that cannot rotate off the schedule, and
// records why.
func stopRotation(ctx context.Context, pool *pgxpool.Pool, tenantID, id, reason string) error {
	return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE vault_accounts SET rotate_at = NULL, last_rotation_error = $2, updated_at = now() WHERE id = $1`, id, reason); err != nil {
			return err
		}
		return db.Audit(ctx, tx, tenantID, "system:vault", "vault.rotation_failed", "vault_account", id,
			map[string]any{"trigger": "schedule", "error": reason})
	})
}
