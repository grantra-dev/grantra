package gateways

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// TokenPrefix starts every token, so it is recognised when it leaks (in a
// shell history, a commit).
const TokenPrefix = "grt_"

// Token is a token Grantra issued for a person and a target. The token
// itself is only returned when it is issued.
type Token struct {
	ID          string    `json:"id"`
	TargetID    string    `json:"target_id"`
	TargetName  string    `json:"target_name"`
	Kind        string    `json:"kind"`
	IdentityID  string    `json:"identity_id"`
	ValidBefore time.Time `json:"valid_before"`
	IssuedAt    time.Time `json:"issued_at"`
	Token       string    `json:"token,omitempty"`
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Issue creates a token for identityID, who must hold access to the target
// (the caller checks the policies). It lasts the target's maximum and never
// past the access it comes from.
func Issue(ctx context.Context, tx pgx.Tx, tenantID, identityID, targetID string) (Token, error) {
	t, err := Get(ctx, tx, targetID)
	if err != nil {
		return Token{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, err
	}
	tok := Token{TargetID: t.ID, TargetName: t.Name, Kind: t.Kind, IdentityID: identityID,
		Token: TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)}
	err = tx.QueryRow(ctx,
		`INSERT INTO gateway_tokens (tenant_id, target_id, identity_id, token_hash, valid_before)
		 SELECT $1, t.id, $3::uuid, $4,
		        CASE WHEN a.n > 0 THEN least(now() + make_interval(hours => t.max_token_hours),
		                                     CASE WHEN NOT a.forever THEN a.until END) END
		 FROM gateway_targets t, LATERAL (
		   SELECT count(*) AS n, max(x.expires_at) AS until, bool_or(x.expires_at IS NULL) AS forever
		   FROM access_assignments x JOIN identities i ON i.id = x.identity_id
		   WHERE x.identity_id = $3::uuid AND i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
		     AND (x.entitlement_id = t.entitlement_id
		          OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = t.entitlement_id))) a
		 WHERE t.id = $2
		 RETURNING id, valid_before, issued_at`,
		tenantID, t.ID, identityID, hashToken(tok.Token)).Scan(&tok.ID, &tok.ValidBefore, &tok.IssuedAt)
	if err != nil {
		var pg interface{ SQLState() string }
		// valid_before is NULL when the person holds no access to it.
		if errors.As(err, &pg) && pg.SQLState() == "23502" {
			return Token{}, apperr.Conflictf("you do not hold access to %s right now", t.Name)
		}
		return Token{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, db.IdentityActor(identityID), "gateway.token_issued", "gateway_target", t.ID,
		map[string]any{"token_id": tok.ID, "valid_before": tok.ValidBefore.UTC()}); err != nil {
		return Token{}, err
	}
	return tok, nil
}

// Grant is what the gateway needs to let a token through to its target.
type Grant struct {
	TokenID       string
	IdentityID    string
	IdentityName  string
	IdentityEmail string
	ValidBefore   time.Time
	Target        Target
}

// Denied is why the gateway turned a token away, safe to tell whoever
// presented it.
type Denied struct{ Msg string }

func (d *Denied) Error() string { return d.Msg }

var (
	// ErrNoAccess is returned when the token's person does not hold access
	// to its target right now.
	ErrNoAccess error = &Denied{"you do not hold access to this target right now"}
	// ErrBadToken is returned for a token that is unknown, expired or
	// revoked.
	ErrBadToken error = &Denied{"the token is unknown, expired or revoked: get a new one in Grantra"}
)

// Authorize checks a token someone presented to the gateway for the target
// of kind named name: Grantra issued it for that target, it has not expired
// or been revoked, and its person is still active and holds access to it.
func Authorize(ctx context.Context, tx pgx.Tx, token, kind, name string) (Grant, error) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 100 {
		return Grant{}, ErrBadToken
	}
	var g Grant
	var targetID string
	var live bool
	err := tx.QueryRow(ctx,
		`SELECT k.id, k.identity_id, i.display_name, coalesce(i.email, ''), k.valid_before, k.target_id,
		        k.revoked_at IS NULL AND k.valid_before > now()
		 FROM gateway_tokens k JOIN identities i ON i.id = k.identity_id
		 WHERE k.token_hash = $1`, hashToken(token)).
		Scan(&g.TokenID, &g.IdentityID, &g.IdentityName, &g.IdentityEmail, &g.ValidBefore, &targetID, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrBadToken
	}
	if err != nil {
		return Grant{}, err
	}
	if !live {
		return Grant{}, ErrBadToken
	}
	if g.Target, err = Get(ctx, tx, targetID); err != nil {
		return Grant{}, err
	}
	if g.Target.Kind != kind || g.Target.Name != name {
		return Grant{}, &Denied{fmt.Sprintf("the token is for %s, not %s", g.Target.Name, name)}
	}
	ok, err := holds(ctx, tx, g.IdentityID, targetID)
	if err != nil {
		return Grant{}, err
	}
	if !ok {
		return Grant{}, ErrNoAccess
	}
	return g, nil
}

func holds(ctx context.Context, tx pgx.Tx, identityID, targetID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT `+heldBy+` FROM gateway_targets t WHERE t.id = $2`, identityID, targetID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// KubePathPrefix starts the path of every cluster on the gateway:
// https://gateway:8443/kubernetes/<target>.
const KubePathPrefix = "/kubernetes/"

// Kubeconfig is a kubeconfig that reaches target through the gateway at
// gatewayAddress (host:port) with token, trusting the PEM caCert.
func Kubeconfig(gatewayAddress, target, token, caCert string) string {
	server := "https://" + gatewayAddress + KubePathPrefix + target
	name := "grantra-" + target
	ca := ""
	if caCert != "" {
		ca = "\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte(caCert))
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster:
    server: %[2]q%[3]s
users:
- name: %[1]s
  user:
    token: %[4]q
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s
current-context: %[1]s
`, name, server, ca, token)
}
