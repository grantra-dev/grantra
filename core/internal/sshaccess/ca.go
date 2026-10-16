// Package sshaccess gives people SSH access to servers without keys of
// their own on those servers. Grantra keeps an SSH certificate authority per
// tenant; servers trust it (TrustedUserCAKeys) and Grantra signs short-lived
// certificates for the people who hold access to a server, valid only while
// that access lasts.
//
// Who may log in to a server (a target) is an entitlement in the "SSH
// servers" application, created with the target. Requests, approvals,
// reviews, separation of duties, just-in-time activation and expiry
// therefore govern SSH like any other access.
//
// Targets reached through the gateway (see the gateway package) are
// recorded: every session's output in asciicast v2, and the commands typed,
// for playback and search in the console.
package sshaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// ErrDisabled is returned when there is no key to seal the CA with.
var ErrDisabled = apperr.Conflictf("SSH access is off: it keeps its certificate authority sealed with the vault key; set %s and restart Grantra", vault.KeyEnv)

// Service signs certificates with the tenant's CA, whose private key it
// seals with keys. Keys may be nil: targets can then be managed but no
// certificate is signed.
type Service struct {
	keys vault.KeyProvider
	now  func() time.Time
}

// New returns the SSH service, sealing its CA with keys.
func New(keys vault.KeyProvider) *Service {
	return &Service{keys: keys, now: time.Now}
}

// Enabled reports whether the service can sign certificates.
func (s *Service) Enabled() bool { return s != nil && s.keys != nil }

func caAAD(tenantID string) []byte { return fmt.Appendf(nil, "grantra-ssh-ca:v1:%s", tenantID) }

// PublicCA returns the CA's public key in authorized_keys format, the line
// servers put in their TrustedUserCAKeys file. It creates the CA the first
// time.
func (s *Service) PublicCA(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var pub string
	err := tx.QueryRow(ctx, `SELECT public_key FROM ssh_cas`).Scan(&pub)
	if errors.Is(err, pgx.ErrNoRows) {
		signer, err := s.signer(ctx, tx, tenantID)
		if err != nil {
			return "", err
		}
		return authorizedLine(signer.PublicKey()), nil
	}
	return pub, err
}

// signer returns the tenant's CA, creating it the first time.
func (s *Service) signer(ctx context.Context, tx pgx.Tx, tenantID string) (ssh.Signer, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	var sealed vault.Sealed
	err := tx.QueryRow(ctx, `SELECT key_id, wrapped_key, nonce, ciphertext FROM ssh_cas`).
		Scan(&sealed.KeyID, &sealed.WrappedKey, &sealed.Nonce, &sealed.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.createCA(ctx, tx, tenantID)
	}
	if err != nil {
		return nil, err
	}
	seed, err := vault.Open(s.keys, sealed, caAAD(tenantID))
	if err != nil {
		return nil, fmt.Errorf("open the SSH certificate authority: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the SSH certificate authority key is damaged")
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
}

func (s *Service) createCA(ctx context.Context, tx pgx.Tx, tenantID string) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	sealed, err := vault.Seal(s.keys, priv.Seed(), caAAD(tenantID))
	if err != nil {
		return nil, err
	}
	// Two requests creating the CA at once keep the first; the other reads
	// it back.
	tag, err := tx.Exec(ctx,
		`INSERT INTO ssh_cas (tenant_id, public_key, key_id, wrapped_key, nonce, ciphertext)
		 VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID, authorizedLine(signer.PublicKey()), sealed.KeyID, sealed.WrappedKey, sealed.Nonce, sealed.Ciphertext)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return s.signer(ctx, tx, tenantID)
	}
	return signer, nil
}

func authorizedLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// ParseCA parses the line PublicCA returns.
func ParseCA(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}
