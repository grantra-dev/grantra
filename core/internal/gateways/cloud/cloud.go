// Package cloud signs people in to cloud consoles (AWS, Azure, Google
// Cloud) with access Grantra grants there for the session only, so nobody
// keeps standing access to the cloud:
//
//   - AWS: Grantra assumes the target's IAM role for the person, with
//     credentials that end with the session, and signs them in to the
//     console with those (and hands them out for the CLI).
//   - Azure: Grantra assigns the person (their Microsoft Entra user) the
//     target's role on its scope, and removes the assignment when the
//     session ends.
//   - Google Cloud: Grantra binds the person (their Google account) to the
//     target's role on its project, with a condition that ends the binding
//     with the session, and removes the binding then too.
//
// Each sign-in is a gateway session. EndSessions, run every 30 seconds,
// takes back what was granted when the session ends: when its time is up,
// the person's access ends, or an admin ends it.
package cloud

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// Endpoints are where the clouds' APIs are. Empty ones are the clouds' own;
// tests point them elsewhere.
type Endpoints struct {
	AWSSTS               string
	AWSSignin            string
	AzureLogin           string
	AzureManagement      string
	MicrosoftGraph       string
	GoogleResourceMgr    string
	HTTPClient           *http.Client
	NoDefaultCredentials bool // tests turn the clouds' default credentials off
}

// Consoles opens cloud console sessions and ends them.
type Consoles struct {
	Pool     *pgxpool.Pool
	TenantID string
	Gateways *gateways.Service
	// Issuer is Grantra's own URL, where AWS sends people whose console
	// session ended.
	Issuer    string
	Endpoints Endpoints
	Log       *slog.Logger
}

func (c *Consoles) httpClient() *http.Client {
	if c.Endpoints.HTTPClient != nil {
		return c.Endpoints.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Consoles) log() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// Opened is a console session: the URL that signs the person in, and on
// AWS the credentials for the CLI too.
type Opened struct {
	SessionID   string          `json:"session_id"`
	URL         string          `json:"url"`
	ValidBefore time.Time       `json:"valid_before"`
	Credentials *AWSCredentials `json:"credentials,omitempty"`
}

// AWSCredentials are temporary credentials of an AWS role.
type AWSCredentials struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	Expiration      time.Time `json:"expiration"`
}

// person is who a console session is for.
type person struct {
	identityID, name, email, sessionID string
}

// granted is what a provider granted.
type granted struct {
	url, account string
	credentials  *AWSCredentials
	// grant is what to take back, or empty when there is nothing (AWS, or
	// a role the person already held).
	grant string
}

type provider interface {
	// open grants p the target's access until until, naming what it grants
	// grant (unique to the session), and returns how to sign in.
	open(ctx context.Context, p person, until time.Time, grant string) (granted, error)
	// url is how to sign in again to a session already granted.
	url(p person) string
	// close takes back what open granted.
	close(ctx context.Context, grant string) error
}

func (c *Consoles) provider(t gateways.Target, username, secret string) (provider, error) {
	switch t.Kind {
	case gateways.AWS:
		return &awsConsole{c: c, t: t, accessKeyID: username, secret: secret}, nil
	case gateways.Azure:
		return &azureConsole{c: c, t: t, clientID: username, secret: secret}, nil
	case gateways.GCP:
		return &gcpConsole{c: c, t: t, key: secret}, nil
	}
	return nil, fmt.Errorf("%s is not a cloud console", t.Name)
}

// credentials opens the target's vault account, if it has one.
func (c *Consoles) credentials(ctx context.Context, tx pgx.Tx, t gateways.Target) (username, secret string, err error) {
	if t.VaultAccountID == "" {
		return "", "", nil
	}
	return c.Gateways.Credentials(ctx, tx, c.TenantID, t)
}

// awsMinSession is the shortest session AWS gives a role.
const awsMinSession = 15 * time.Minute

// Refused is a cloud refusing what Grantra asked of it, safe to show the
// person.
type Refused struct{ Msg string }

func (r *Refused) Error() string { return r.Msg }

// notGranted marks an error that came before anything was granted, or with
// the cloud saying it granted nothing: there is nothing to take back.
type notGranted struct{ error }

func (e notGranted) Unwrap() error { return e.error }

// definite says whether an HTTP status means the cloud did not do what it
// was asked (as opposed to an answer lost on the way or a server error).
func definite(status int) bool { return status >= 400 && status < 500 }

// Open signs identityID in to the cloud console target, who must hold
// access to it (the caller checks the policies). On Azure and Google Cloud,
// a session of theirs still running there is reused: the cloud holds one
// grant per person and role. One that is ending makes them wait for it.
func (c *Consoles) Open(ctx context.Context, identityID, targetID, clientAddress string) (Opened, error) {
	var (
		t        gateways.Target
		p        person
		tok      gateways.Token
		user     string
		secret   string
		grant    string
		existing *gateways.Session
	)
	err := db.InTenant(ctx, c.Pool, c.TenantID, func(tx pgx.Tx) (err error) {
		if t, err = gateways.Get(ctx, tx, targetID); err != nil {
			return err
		}
		if !gateways.Cloud(t.Kind) {
			return apperr.Invalidf("%s is not a cloud console", t.Name)
		}
		p.identityID = identityID
		if err := tx.QueryRow(ctx, `SELECT display_name, coalesce(email, '') FROM identities WHERE id = $1`, identityID).
			Scan(&p.name, &p.email); err != nil {
			return err
		}
		if p.email == "" {
			return apperr.Conflictf("you have no email in Grantra, and the cloud knows you by it: ask an admin to add it")
		}
		if t.Kind != gateways.AWS {
			running, err := gateways.ListSessions(ctx, tx, gateways.SessionFilter{TargetID: t.ID, IdentityID: identityID, Active: true, Limit: 1})
			if err != nil {
				return err
			}
			if len(running) == 1 {
				s := running[0]
				if s.Error == "" && !s.TerminateRequested && s.ValidBefore != nil && time.Until(*s.ValidBefore) > time.Minute {
					existing = &s
					return nil
				}
				// It is ending, or failed and its grant is still being
				// taken back. The cloud holds one grant per person and
				// role, which a second session would share and lose with
				// this one's end.
				return apperr.Conflictf("your last session on %s is still ending: open it again in a minute", t.Name)
			}
		}
		if user, secret, err = c.credentials(ctx, tx, t); err != nil {
			return err
		}
		if tok, err = gateways.Issue(ctx, tx, c.TenantID, identityID, t.ID); err != nil {
			return err
		}
		if t.Kind == gateways.AWS && time.Until(tok.ValidBefore) < awsMinSession {
			return apperr.Conflictf("AWS console sessions last at least 15 minutes, and your access to %s ends sooner", t.Name)
		}
		if t.Kind != gateways.AWS {
			grant = "grantra-" + randomHex(16)
		}
		p.sessionID, err = gateways.StartSession(ctx, tx, c.TenantID, gateways.Session{TargetID: t.ID, TargetName: t.Name, Kind: t.Kind,
			IdentityID: identityID, TokenID: tok.ID, ClientAddress: clientAddress, Gateway: "console", CloudGrant: grant}, t.VaultAccountID)
		return err
	})
	if err != nil {
		return Opened{}, err
	}
	if existing != nil {
		p.sessionID = existing.ID
		pr, err := c.provider(t, "", "")
		if err != nil {
			return Opened{}, err
		}
		return Opened{SessionID: existing.ID, URL: pr.url(p), ValidBefore: *existing.ValidBefore}, nil
	}

	// Recording what happened must outlive the person closing the tab.
	bg := context.WithoutCancel(ctx)
	pr, err := c.provider(t, user, secret)
	if err == nil {
		var g granted
		octx, cancel := context.WithTimeout(ctx, time.Minute)
		g, err = pr.open(octx, p, tok.ValidBefore, grant)
		cancel()
		if err == nil {
			err = db.InTenant(bg, c.Pool, c.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(bg, `UPDATE gateway_sessions SET account = $2, cloud_grant = $3 WHERE id = $1`,
					p.sessionID, db.Nullable(g.account), db.Nullable(g.grant))
				return err
			})
			if err == nil {
				return Opened{SessionID: p.sessionID, URL: g.url, ValidBefore: tok.ValidBefore, Credentials: g.credentials}, nil
			}
		}
	}
	// It failed: take back whatever may have been granted, and end the
	// session. If taking back fails too, the session stays for EndSessions
	// to try again.
	c.log().WarnContext(ctx, "could not open a cloud console session", "target", t.Name, "session", p.sessionID, "err", err)
	var ng notGranted
	cleaned := grant == "" || pr == nil || errors.As(err, &ng)
	if !cleaned {
		cctx, cancel := context.WithTimeout(bg, time.Minute)
		cleaned = pr.close(cctx, grant) == nil
		cancel()
	}
	errText := gateways.CleanText(err.Error())
	endErr := db.InTenant(bg, c.Pool, c.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(bg, `UPDATE gateway_tokens SET revoked_at = now(), revoked_by = 'grantra' WHERE id = $1`, tok.ID); err != nil {
			return err
		}
		if !cleaned {
			_, err := tx.Exec(bg, `UPDATE gateway_sessions SET error = $2 WHERE id = $1`, p.sessionID, errText)
			return err
		}
		return gateways.EndSession(bg, tx, c.TenantID, p.sessionID, gateways.EndFailed, errText)
	})
	if endErr != nil {
		c.log().ErrorContext(ctx, "could not record a failed cloud console session", "session", p.sessionID, "err", endErr)
	}
	var refused *Refused
	if errors.As(err, &refused) {
		return Opened{}, apperr.Conflictf("%s", refused.Msg)
	}
	return Opened{}, err
}

// EndSessions ends the console sessions whose time is up, whose person lost
// the access or that an admin ended, taking back what was granted for them
// first. It returns how many ended.
func (c *Consoles) EndSessions(ctx context.Context) (int, error) {
	type due struct {
		gateways.Session
		reason string
		target gateways.Target
		user   string
		secret string
	}
	var list []due
	err := db.InTenant(ctx, c.Pool, c.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM gateway_sessions WHERE ended_at IS NULL AND kind IN ('aws', 'azure', 'gcp')`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			reason, err := gateways.Heartbeat(ctx, tx, id)
			if err != nil {
				return err
			}
			if reason == "" || reason == gateways.EndLost {
				continue
			}
			s, err := gateways.GetSession(ctx, tx, id)
			if err != nil {
				return err
			}
			// What AWS issued stays valid until it expires: the session
			// runs until then.
			if s.Kind == gateways.AWS && s.ValidBefore != nil && time.Now().Before(*s.ValidBefore) {
				continue
			}
			if s.Error != "" {
				// Opening it failed, and what it granted is still there.
				reason = gateways.EndFailed
			}
			d := due{Session: s, reason: reason}
			if s.CloudGrant != "" {
				if s.TargetID == "" {
					// The target cannot go while it has running console
					// sessions; this one is past saving.
					d.Error = "the target was deleted before its grant could be taken back"
				} else {
					if d.target, err = gateways.Get(ctx, tx, s.TargetID); err != nil {
						return err
					}
					if d.user, d.secret, err = c.credentials(ctx, tx, d.target); err != nil {
						return err
					}
				}
			}
			list = append(list, d)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	ended := 0
	var errs []error
	for _, d := range list {
		if d.CloudGrant != "" && d.target.ID != "" {
			pr, err := c.provider(d.target, d.user, d.secret)
			if err == nil {
				cctx, cancel := context.WithTimeout(ctx, time.Minute)
				err = pr.close(cctx, d.CloudGrant)
				cancel()
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("take back the access of console session %s on %s: %w", d.ID, d.TargetName, err))
				continue
			}
		}
		err := db.InTenant(ctx, c.Pool, c.TenantID, func(tx pgx.Tx) error {
			return gateways.EndSession(ctx, tx, c.TenantID, d.ID, d.reason, d.Error)
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ended++
	}
	return ended, errors.Join(errs...)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck // never fails
	return hex.EncodeToString(b)
}
