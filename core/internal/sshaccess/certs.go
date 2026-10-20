package sshaccess

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Certificate is one certificate Grantra signed for a person.
type Certificate struct {
	ID          string    `json:"id"`
	Serial      uint64    `json:"serial"`
	TargetID    string    `json:"target_id"`
	TargetName  string    `json:"target_name"`
	Login       string    `json:"login"`
	IdentityID  string    `json:"identity_id"`
	Principal   string    `json:"principal"`
	Fingerprint string    `json:"key_fingerprint"`
	ViaGateway  bool      `json:"via_gateway"`
	ValidAfter  time.Time `json:"valid_after"`
	ValidBefore time.Time `json:"valid_before"`
	IssuedAt    time.Time `json:"issued_at"`
	// Certificate is the signed certificate, in the format ssh reads from
	// id_ed25519-cert.pub. Only returned when it is signed.
	Certificate string `json:"certificate,omitempty"`
}

// KeyIDPrefix starts the key id of every certificate Grantra signs for a
// person; the certificate's id follows.
const KeyIDPrefix = "grantra:"

// clockSkew backdates certificates so a server whose clock runs a little
// behind still accepts them.
const clockSkew = time.Minute

// Issue signs a certificate for publicKey, an OpenSSH public key of
// identityID, who must hold access to the target (the caller checks the
// policies). It lasts the target's maximum and never past the access it
// comes from. Its principal is Principal(target name), whether it is used
// at the gateway or straight at the server.
func (s *Service) Issue(ctx context.Context, tx pgx.Tx, tenantID, identityID, targetID, publicKey string) (Certificate, error) {
	if !s.Enabled() {
		return Certificate{}, ErrDisabled
	}
	key, err := parseUserKey(publicKey)
	if err != nil {
		return Certificate{}, err
	}
	t, err := Get(ctx, tx, targetID)
	if err != nil {
		return Certificate{}, err
	}
	signer, err := s.signer(ctx, tx, tenantID, PeopleCA)
	if err != nil {
		return Certificate{}, err
	}
	c := Certificate{TargetID: t.ID, TargetName: t.Name, Login: t.Login, IdentityID: identityID, ViaGateway: t.ViaGateway,
		Fingerprint: ssh.FingerprintSHA256(key), Principal: Principal(t.Name)}
	var serial int64
	err = tx.QueryRow(ctx,
		`INSERT INTO ssh_certificates (tenant_id, target_id, identity_id, principal, key_fingerprint, valid_after, valid_before)
		 SELECT $1, t.id, $3::uuid, $4, $5, now() - make_interval(secs => $6),
		        CASE WHEN a.n > 0 THEN least(now() + make_interval(hours => t.max_certificate_hours),
		                                     CASE WHEN NOT a.forever THEN a.until END) END
		 FROM ssh_targets t, LATERAL (`+catalog.HeldWindowSQL(`$3::uuid`, `t.entitlement_id`)+`) a
		 WHERE t.id = $2
		 RETURNING id, serial, valid_after, valid_before, issued_at`,
		tenantID, t.ID, identityID, c.Principal, c.Fingerprint, clockSkew.Seconds()).
		Scan(&c.ID, &serial, &c.ValidAfter, &c.ValidBefore, &c.IssuedAt)
	if err != nil {
		var pg interface{ SQLState() string }
		// valid_before is NULL when the person holds no access to it.
		if errors.As(err, &pg) && pg.SQLState() == "23502" {
			return Certificate{}, apperr.Conflictf("you do not hold access to %s right now", t.Name)
		}
		return Certificate{}, err
	}
	c.Serial = uint64(serial)
	cert := &ssh.Certificate{
		Key:             key,
		Serial:          c.Serial,
		CertType:        ssh.UserCert,
		KeyId:           KeyIDPrefix + c.ID,
		ValidPrincipals: []string{c.Principal},
		ValidAfter:      uint64(c.ValidAfter.Unix()),
		ValidBefore:     uint64(c.ValidBefore.Unix()),
		Permissions:     ssh.Permissions{Extensions: map[string]string{"permit-pty": ""}},
	}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		return Certificate{}, err
	}
	c.Certificate = authorizedLine(cert)
	if err := db.Audit(ctx, tx, tenantID, db.IdentityActor(identityID), "ssh.certificate_issued", "ssh_target", t.ID,
		map[string]any{"certificate_id": c.ID, "serial": c.Serial, "principal": c.Principal, "key_fingerprint": c.Fingerprint,
			"valid_before": c.ValidBefore.UTC(), "via_gateway": c.ViaGateway}); err != nil {
		return Certificate{}, err
	}
	return c, nil
}

// parseUserKey accepts one OpenSSH public key: Ed25519, ECDSA or RSA of at
// least 2048 bits, not a certificate.
func parseUserKey(line string) (ssh.PublicKey, error) {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 16<<10 {
		return nil, apperr.Invalidf("public_key must be an OpenSSH public key, such as the contents of ~/.ssh/id_ed25519.pub")
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || strings.TrimSpace(string(rest)) != "" {
		return nil, apperr.Invalidf("public_key must be one OpenSSH public key, such as the contents of ~/.ssh/id_ed25519.pub")
	}
	if _, ok := key.(*ssh.Certificate); ok {
		return nil, apperr.Invalidf("public_key must be a public key, not a certificate")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		ck, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return nil, apperr.Invalidf("unsupported public key")
		}
		if rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey); !ok || rk.N.BitLen() < 2048 {
			return nil, apperr.Invalidf("RSA keys must have at least 2048 bits; an Ed25519 key is better (ssh-keygen -t ed25519)")
		}
	default:
		return nil, apperr.Invalidf("use an Ed25519, ECDSA or RSA key (ssh-keygen -t ed25519), not %s", key.Type())
	}
	return key, nil
}
