// Package sshaccess gives people SSH access to servers without keys of
// their own on those servers. Grantra keeps two SSH certificate authorities
// per tenant and signs short-lived certificates with them:
//
//   - The people CA signs certificates for the people who hold access to a
//     server, valid only while that access lasts. Their principal is
//     "grantra:<target name>", which is never a Unix account name, so a
//     server only accepts one where its AuthorizedPrincipalsFile names that
//     target. The gateway trusts this CA too.
//   - The gateway CA signs the two-minute certificates the gateway logs in
//     to targets with. Servers reached through the gateway trust only this
//     CA, so nobody reaches them around it, and no certificate a person
//     holds works on them.
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

// The tenant's two certificate authorities.
const (
	PeopleCA  = "people"
	GatewayCA = "gateway"
)

func caAAD(tenantID, purpose string) []byte {
	return fmt.Appendf(nil, "grantra-ssh-ca:v1:%s:%s", tenantID, purpose)
}

// PublicCA returns a CA's public key in authorized_keys format, the line
// servers put in their TrustedUserCAKeys file. It creates the CA the first
// time.
func (s *Service) PublicCA(ctx context.Context, tx pgx.Tx, tenantID, purpose string) (string, error) {
	var pub string
	err := tx.QueryRow(ctx, `SELECT public_key FROM ssh_cas WHERE purpose = $1`, purpose).Scan(&pub)
	if errors.Is(err, pgx.ErrNoRows) {
		signer, err := s.signer(ctx, tx, tenantID, purpose)
		if err != nil {
			return "", err
		}
		return authorizedLine(signer.PublicKey()), nil
	}
	return pub, err
}

// signer returns one of the tenant's CAs, creating it the first time.
func (s *Service) signer(ctx context.Context, tx pgx.Tx, tenantID, purpose string) (ssh.Signer, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	var sealed vault.Sealed
	err := tx.QueryRow(ctx, `SELECT key_id, wrapped_key, nonce, ciphertext FROM ssh_cas WHERE purpose = $1`, purpose).
		Scan(&sealed.KeyID, &sealed.WrappedKey, &sealed.Nonce, &sealed.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.createCA(ctx, tx, tenantID, purpose)
	}
	if err != nil {
		return nil, err
	}
	seed, err := vault.Open(s.keys, sealed, caAAD(tenantID, purpose))
	if err != nil {
		return nil, fmt.Errorf("open the SSH certificate authority: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the SSH certificate authority key is damaged")
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
}

func (s *Service) createCA(ctx context.Context, tx pgx.Tx, tenantID, purpose string) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	sealed, err := vault.Seal(s.keys, priv.Seed(), caAAD(tenantID, purpose))
	if err != nil {
		return nil, err
	}
	// Two requests creating the CA at once keep the first; the other reads
	// it back.
	tag, err := tx.Exec(ctx,
		`INSERT INTO ssh_cas (tenant_id, purpose, public_key, key_id, wrapped_key, nonce, ciphertext)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (tenant_id, purpose) DO NOTHING`,
		tenantID, purpose, authorizedLine(signer.PublicKey()), sealed.KeyID, sealed.WrappedKey, sealed.Nonce, sealed.Ciphertext)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return s.signer(ctx, tx, tenantID, purpose)
	}
	return signer, nil
}

func authorizedLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// Principal is the principal of the certificates people get for a target.
// The colon keeps it from ever matching a Unix account name, so a server
// accepts it only where its AuthorizedPrincipalsFile lists it.
func Principal(targetName string) string { return "grantra:" + targetName }

// ParseCA parses the line PublicCA returns.
func ParseCA(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}
