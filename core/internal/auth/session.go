package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// login is what the provider told us about the person signing in.
type login struct {
	issuer, subject string
	email           string
	emailVerified   bool
	idToken         string
}

type outcome int

const (
	signedIn outcome = iota
	notEnrolled
	inactive
)

// openSession finds the identity behind a provider login and, when it is
// active, stores a session for token. Every attempt lands in the audit log.
//
// Identities come from HR and connectors, never from logins: someone the
// provider knows but Grantra does not is refused, not created.
func (s *Service) openSession(ctx context.Context, l login, token string) (outcome, error) {
	res := signedIn
	err := db.InTenant(ctx, s.pool, s.tenantID, func(tx pgx.Tx) error {
		identityID, err := s.linkLogin(ctx, tx, l)
		if err != nil {
			return err
		}
		actor := "oidc:" + l.issuer + "#" + l.subject
		if identityID == "" {
			res = notEnrolled
			return audit(ctx, tx, s.tenantID, actor, "auth.login_denied", "", map[string]any{"reason": "not_enrolled", "email": l.email})
		}

		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM identities WHERE id = $1`, identityID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			res = inactive
			return audit(ctx, tx, s.tenantID, actor, "auth.login_denied", identityID, map[string]any{"reason": "identity_" + status})
		}

		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO sessions (tenant_id, token_hash, identity_id, id_token, expires_at)
			 VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))`,
			s.tenantID, hash(token), identityID, l.idToken, s.cfg.SessionMax.Seconds()); err != nil {
			return err
		}
		return audit(ctx, tx, s.tenantID, "identity:"+identityID, "auth.login", identityID, map[string]any{"issuer": l.issuer})
	})
	return res, err
}

// linkLogin returns the identity linked to the login, linking it by verified
// email on first sight. It returns "" when no single identity matches.
func (s *Service) linkLogin(ctx context.Context, tx pgx.Tx, l login) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`UPDATE identity_logins SET last_login_at = now()
		 WHERE issuer = $1 AND subject = $2 RETURNING identity_id`, l.issuer, l.subject).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	// An unverified email could belong to anyone, so it never links.
	if !l.emailVerified || l.email == "" {
		return "", nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM identities WHERE lower(email) = lower($1) LIMIT 2`, l.email)
	if err != nil {
		return "", err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		if len(ids) > 1 {
			s.log.WarnContext(ctx, "login email matches several identities; not linking", "email", l.email)
		}
		return "", nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO identity_logins (tenant_id, issuer, subject, identity_id) VALUES ($1, $2, $3, $4)`,
		s.tenantID, l.issuer, l.subject, ids[0]); err != nil {
		return "", fmt.Errorf("link login: %w", err)
	}
	return ids[0], nil
}

// Lookup returns the person signed in on r. A session ends when it passes its
// maximum age, sits idle too long, or its identity stops being active, so
// someone HR offboards loses access at their next request.
func (s *Service) Lookup(r *http.Request) (Principal, bool, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return Principal{}, false, nil
	}
	ctx := r.Context()
	var (
		p     Principal
		valid bool
	)
	err = db.InTenant(ctx, s.pool, s.tenantID, func(tx pgx.Tx) error {
		var status string
		var expired bool
		err := tx.QueryRow(ctx,
			`SELECT i.id, i.display_name, coalesce(i.email, ''), i.status,
			        s.expires_at <= now() OR s.last_seen_at <= now() - make_interval(secs => $2)
			 FROM sessions s JOIN identities i ON i.id = s.identity_id
			 WHERE s.token_hash = $1`, hash(c.Value), s.cfg.SessionIdle.Seconds()).
			Scan(&p.IdentityID, &p.DisplayName, &p.Email, &status, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if expired || status != "active" {
			_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hash(c.Value))
			return err
		}
		valid = true
		// Refresh the idle timer, at most once a minute per session.
		_, err = tx.Exec(ctx,
			`UPDATE sessions SET last_seen_at = now()
			 WHERE token_hash = $1 AND last_seen_at < now() - interval '1 minute'`, hash(c.Value))
		return err
	})
	if err != nil || !valid {
		return Principal{}, false, err
	}
	return p, true, nil
}

// closeSession deletes the session for token and returns its ID token, used
// as a hint when ending the provider session.
func (s *Service) closeSession(ctx context.Context, token string) (string, error) {
	var idToken string
	err := db.InTenant(ctx, s.pool, s.tenantID, func(tx pgx.Tx) error {
		var identityID string
		err := tx.QueryRow(ctx,
			`DELETE FROM sessions WHERE token_hash = $1 RETURNING identity_id, id_token`, hash(token)).
			Scan(&identityID, &idToken)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, s.tenantID, "identity:"+identityID, "auth.logout", identityID, nil)
	})
	return idToken, err
}

func audit(ctx context.Context, tx pgx.Tx, tenantID, actor, action, identityID string, details map[string]any) error {
	return db.Audit(ctx, tx, tenantID, actor, action, "identity", identityID, details)
}
