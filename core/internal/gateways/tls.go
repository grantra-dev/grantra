package gateways

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// ErrDisabled is returned when there is no key to seal the CA with.
var ErrDisabled = apperr.Conflictf("the gateway is off: it keeps its TLS certificate authority and the credentials it logs in with sealed with the vault key; set %s and restart Grantra", vault.KeyEnv)

// Service keeps the CA of the gateway's TLS certificates and opens the
// credentials the gateway logs in to targets with, both sealed with keys.
// Keys may be nil: targets can then be managed but nobody connects.
type Service struct {
	keys  vault.KeyProvider
	vault *vault.Vault
	now   func() time.Time
}

// New returns the gateway service, sealing with keys.
func New(keys vault.KeyProvider) *Service {
	return &Service{keys: keys, vault: vault.New(keys, nil), now: time.Now}
}

// Enabled reports whether the service can sign certificates and open
// credentials.
func (s *Service) Enabled() bool { return s != nil && s.keys != nil }

func caAAD(tenantID string) []byte { return fmt.Appendf(nil, "grantra-gateway-tls-ca:v1:%s", tenantID) }

// Credentials opens the login and password (or token) the gateway uses on
// a target.
func (s *Service) Credentials(ctx context.Context, tx pgx.Tx, tenantID string, t Target) (username, secret string, err error) {
	if t.VaultAccountID == "" {
		return "", "", errors.New(t.Name + " has no vault account to log in with")
	}
	return s.vault.GatewaySecret(ctx, tx, tenantID, t.VaultAccountID)
}

// CACertificate returns the PEM certificate of the CA the gateway's TLS
// certificates chain to, creating the CA the first time.
func (s *Service) CACertificate(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var cert string
	err := tx.QueryRow(ctx, `SELECT certificate FROM gateway_tls_cas`).Scan(&cert)
	if errors.Is(err, pgx.ErrNoRows) {
		ca, _, err := s.ca(ctx, tx, tenantID)
		if err != nil {
			return "", err
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})), nil
	}
	return cert, err
}

// ca returns the tenant's CA, creating it the first time.
func (s *Service) ca(ctx context.Context, tx pgx.Tx, tenantID string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if !s.Enabled() {
		return nil, nil, ErrDisabled
	}
	var certPEM string
	var sealed vault.Sealed
	err := tx.QueryRow(ctx, `SELECT certificate, key_id, wrapped_key, nonce, ciphertext FROM gateway_tls_cas`).
		Scan(&certPEM, &sealed.KeyID, &sealed.WrappedKey, &sealed.Nonce, &sealed.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.createCA(ctx, tx, tenantID)
	}
	if err != nil {
		return nil, nil, err
	}
	der, err := vault.Open(s.keys, sealed, caAAD(tenantID))
	if err != nil {
		return nil, nil, fmt.Errorf("open the gateway certificate authority: %w", err)
	}
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return nil, nil, errors.New("the gateway certificate authority key is damaged")
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, nil, errors.New("the gateway certificate authority certificate is damaged")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func (s *Service) createCA(ctx context.Context, tx pgx.Tx, tenantID string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "Grantra gateway CA", Organization: []string{"Grantra"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	sealed, err := vault.Seal(s.keys, keyDER, caAAD(tenantID))
	if err != nil {
		return nil, nil, err
	}
	// Two gateways creating the CA at once keep the first; the other reads
	// it back.
	tag, err := tx.Exec(ctx,
		`INSERT INTO gateway_tls_cas (tenant_id, certificate, key_id, wrapped_key, nonce, ciphertext)
		 VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		sealed.KeyID, sealed.WrappedKey, sealed.Nonce, sealed.Ciphertext)
	if err != nil {
		return nil, nil, err
	}
	if tag.RowsAffected() == 0 {
		return s.ca(ctx, tx, tenantID)
	}
	return cert, key, nil
}

// ServingCertDays is how long the gateway's own TLS certificates last. It
// gets a new one well before.
const ServingCertDays = 30

// ServingCertificate signs a fresh TLS certificate for the gateway, for
// names (host names or IPs).
func (s *Service) ServingCertificate(ctx context.Context, tx pgx.Tx, tenantID string, names []string) (*tls.Certificate, error) {
	if len(names) == 0 {
		return nil, errors.New("the gateway's TLS certificate needs at least one name")
	}
	ca, caKey, err := s.ca(ctx, tx, tenantID)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: names[0], Organization: []string{"Grantra"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, ServingCertDays),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// Rewrap re-wraps the data key of the CA when the current key did not wrap
// it. It returns how many it re-wrapped (0 or 1).
func Rewrap(ctx context.Context, tx pgx.Tx, tenantID string, kp vault.KeyProvider) (int, error) {
	var keyID string
	var wrapped []byte
	err := tx.QueryRow(ctx, `SELECT key_id, wrapped_key FROM gateway_tls_cas WHERE key_id <> $1 FOR UPDATE`, kp.KeyID()).
		Scan(&keyID, &wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rewrapped, err := vault.RewrapKey(kp, keyID, wrapped, caAAD(tenantID))
	if err != nil {
		return 0, fmt.Errorf("gateway TLS CA: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE gateway_tls_cas SET key_id = $1, wrapped_key = $2`, kp.KeyID(), rewrapped); err != nil {
		return 0, err
	}
	return 1, nil
}
