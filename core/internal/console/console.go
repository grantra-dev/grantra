// Package console serves the JSON API behind the Grantra console. Every
// endpoint needs a signed-in person, and every decision about what that
// person may see or change goes through the Cedar policies in package authz.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// API holds the console endpoints for one tenant.
type API struct {
	pool     *pgxpool.Pool
	tenantID string
	authz    *authz.Authorizer
	log      *slog.Logger
	secrets  connector.SecretResolver

	// EmailEnabled says whether notification email is set up, so the
	// console can tell people when their settings have no effect.
	EmailEnabled bool
}

// New returns the console API for the given tenant.
func New(pool *pgxpool.Pool, tenantID string, az *authz.Authorizer, log *slog.Logger) *API {
	return &API{pool: pool, tenantID: tenantID, authz: az, log: log, secrets: connector.EnvSecrets}
}

// Register adds the console routes to mux. require must reject requests that
// have no signed-in person and store the person with auth.ContextWithPrincipal.
func (a *API) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	route := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, require(sameOrigin(h)))
	}
	route("GET /api/v1/me", a.me)
	route("GET /api/v1/me/notifications", a.getNotifications)
	route("PUT /api/v1/me/notifications/{kind}", a.setNotification)
	route("GET /api/v1/identities", a.listIdentities)
	route("GET /api/v1/identities/{id}", a.getIdentity)
	route("GET /api/v1/audit-events", a.listAuditEvents)
	route("GET /api/v1/platform-roles", a.listPlatformRoles)
	route("PUT /api/v1/platform-roles/{role}/members/{identity}", a.grantPlatformRole)
	route("DELETE /api/v1/platform-roles/{role}/members/{identity}", a.revokePlatformRole)
	route("GET /api/v1/identity-sources", a.listSources)
	route("POST /api/v1/identity-sources", a.createSource)
	route("GET /api/v1/identity-sources/{id}", a.getSource)
	route("GET /api/v1/identity-sources/{id}/runs", a.listRuns)
	route("POST /api/v1/identity-sources/{id}/runs", a.syncSource)
	route("GET /api/v1/lifecycle-events", a.listLifecycleEvents)
	route("GET /api/v1/identities/{id}/access", a.identityAccess)
	route("GET /api/v1/applications", a.listApplications)
	route("POST /api/v1/applications", a.createApplication)
	route("GET /api/v1/applications/{id}/entitlements", a.listEntitlements)
	route("POST /api/v1/applications/{id}/entitlements", a.createEntitlement)
	route("GET /api/v1/roles", a.listRoles)
	route("POST /api/v1/roles", a.saveRole)
	route("GET /api/v1/roles/{id}", a.getRole)
	route("PUT /api/v1/roles/{id}", a.saveRole)
	route("GET /api/v1/access-rules", a.listAccessRules)
	route("POST /api/v1/access-rules", a.saveAccessRule)
	route("POST /api/v1/access-rules/preview", a.previewAccessRule)
	route("GET /api/v1/access-rules/{id}", a.getAccessRule)
	route("PUT /api/v1/access-rules/{id}", a.saveAccessRule)
	route("DELETE /api/v1/access-rules/{id}", a.deleteAccessRule)
	route("PUT /api/v1/applications/{id}/connector", a.setConnector)
	route("POST /api/v1/applications/{id}/aggregations", a.aggregate)
	route("PUT /api/v1/applications/{id}/provisioning", a.setProvisioning)
	route("POST /api/v1/applications/{id}/provisioning-runs", a.provision)
	route("GET /api/v1/applications/{id}/runs", a.listConnectorRuns)
	route("GET /api/v1/applications/{id}/accounts", a.listAccounts)
	route("PUT /api/v1/accounts/{id}/identity", a.linkAccount)
	route("GET /api/v1/identities/{id}/accounts", a.identityAccounts)
	route("GET /api/v1/requestable-access", a.listRequestable)
	route("GET /api/v1/access-requests", a.listRequests)
	route("POST /api/v1/access-requests", a.createRequest)
	route("POST /api/v1/access-requests/preview", a.previewRequest)
	route("GET /api/v1/access-requests/{id}", a.getRequest)
	route("POST /api/v1/access-requests/{id}/decision", a.decideRequest)
	route("POST /api/v1/access-requests/{id}/cancel", a.cancelRequest)
	route("GET /api/v1/review-campaigns", a.listCampaigns)
	route("POST /api/v1/review-campaigns", a.launchCampaign)
	route("GET /api/v1/review-campaigns/{id}", a.getCampaign)
	route("POST /api/v1/review-campaigns/{id}/close", a.closeCampaign)
	route("GET /api/v1/review-items", a.listReviewItems)
	route("POST /api/v1/review-items/{id}/decision", a.decideReviewItem)
	route("GET /api/v1/sod-policies", a.listSoDPolicies)
	route("POST /api/v1/sod-policies", a.saveSoDPolicy)
	route("GET /api/v1/sod-policies/{id}", a.getSoDPolicy)
	route("PUT /api/v1/sod-policies/{id}", a.saveSoDPolicy)
	route("DELETE /api/v1/sod-policies/{id}", a.deleteSoDPolicy)
	route("GET /api/v1/sod-violations", a.listSoDViolations)
	route("POST /api/v1/sod-violations/scan", a.scanSoD)
	route("POST /api/v1/sod-violations/{id}/exception", a.exceptSoDViolation)
	route("GET /api/v1/login", a.getLogin)
	route("POST /api/v1/login/apps", a.createLoginApp)
	route("POST /api/v1/login/apps/{id}/secret", a.rotateLoginAppSecret)
	route("PUT /api/v1/login/policy", a.setLoginPolicy)
	route("POST /api/v1/login/identity-providers", a.createIdentityProvider)
	route("PUT /api/v1/login/identity-providers/{alias}/domains", a.setIdentityProviderDomains)
	route("DELETE /api/v1/login/identity-providers/{alias}", a.deleteIdentityProvider)
	route("POST /api/v1/login/import", a.importToLogin)
	route("GET /api/v1/applications/{id}/authz", a.getAppAuthz)
	route("PUT /api/v1/applications/{id}/authz/policies", a.setAppAuthzPolicies)
	route("POST /api/v1/applications/{id}/authz/keys", a.createAppAPIKey)
	route("DELETE /api/v1/applications/{id}/authz/keys/{key}", a.revokeAppAPIKey)
	route("POST /api/v1/applications/{id}/authz/test", a.testAppAuthz)

	// Apps' backends ask with an API key, not a session.
	mux.HandleFunc("POST /api/v1/authz/check", a.authzCheck)
}

// sameOrigin refuses state-changing requests that a browser marks as coming
// from another site. The SameSite=Lax session cookie already keeps those out;
// this is a second lock on the same door.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			switch r.Header.Get("Sec-Fetch-Site") {
			case "", "same-origin", "none":
			default:
				writeError(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// errDenied ends a transaction whose request the policies refused.
var errDenied = errors.New("denied")

// inTenant runs fn in a tenant transaction with the signed-in person loaded
// as a policy subject.
func (a *API) inTenant(r *http.Request, fn func(tx pgx.Tx, me authz.Subject) error) error {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		return errors.New("console handler reached without a signed-in person")
	}
	return db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		me, err := authz.LoadSubject(r.Context(), tx, p.IdentityID)
		if err != nil {
			return err
		}
		return fn(tx, me)
	})
}

// check asks the policies about req. Refused writes are recorded in the audit
// log, from their own transaction so that they survive the refusal.
func (a *API) check(ctx context.Context, req authz.Request) error {
	d := a.authz.Authorize(req)
	if len(d.Errors) > 0 {
		a.log.ErrorContext(ctx, "policy evaluation errors", "action", req.Action, "errors", d.Errors)
	}
	if d.Allowed {
		return nil
	}
	a.log.InfoContext(ctx, "request denied", "identity", req.Principal.ID, "action", req.Action, "policies", d.Policies)
	if req.Action.Writes() {
		details := map[string]any{"action": string(req.Action), "policies": d.Policies}
		target := ""
		if req.Target != nil {
			target = req.Target.ID
		}
		actx := context.WithoutCancel(ctx)
		err := db.InTenant(actx, a.pool, a.tenantID, func(tx pgx.Tx) error {
			return db.Audit(actx, tx, a.tenantID, "identity:"+req.Principal.ID, "authz.denied", "identity", target, details)
		})
		if err != nil {
			a.log.ErrorContext(ctx, "audit denial", "err", err)
		}
	}
	return errDenied
}

// fail turns an error from a handler into a response.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errDenied):
		writeError(w, http.StatusForbidden, "you do not have permission to do this")
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, lifecycle.ErrNotFound), errors.Is(err, apperr.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, authz.ErrLastAdmin):
		writeError(w, http.StatusConflict, "this is the last active admin; grant the role to someone else first")
	case errors.Is(err, authz.ErrInactive):
		writeError(w, http.StatusConflict, "platform roles can only be granted to active identities")
	case errors.Is(err, lifecycle.ErrNameTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, new(*lifecycle.InvalidError)), errors.As(err, new(*apperr.Invalid)):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, new(*apperr.Conflict)):
		writeError(w, http.StatusConflict, err.Error())
	default:
		a.log.ErrorContext(r.Context(), "console request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

type me struct {
	auth.Principal
	PlatformRoles []string `json:"platform_roles"`
	// ManagesPeople and OwnsApplications come from the data, not from an
	// assignment, and decide which console sections the person sees.
	ManagesPeople    bool `json:"manages_people"`
	OwnsApplications bool `json:"owns_applications"`
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	out := me{Principal: p}
	err := a.inTenant(r, func(tx pgx.Tx, s authz.Subject) error {
		out.PlatformRoles = nonNil(s.Roles)
		return tx.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM identities WHERE manager_id = $1 AND status <> 'terminated'),
			        EXISTS (SELECT 1 FROM applications WHERE owner_id = $1)`, s.ID).
			Scan(&out.ManagesPeople, &out.OwnsApplications)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type identity struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	ManagerID   string `json:"manager_id,omitempty"`
	// SourceID and ExternalID say which HR source owns the identity and the
	// employee id it has there.
	SourceID   string         `json:"source_id,omitempty"`
	ExternalID string         `json:"external_id,omitempty"`
	StartDate  string         `json:"start_date,omitempty"`
	EndDate    string         `json:"end_date,omitempty"`
	Attributes map[string]any `json:"attributes"`
	CreatedAt  time.Time      `json:"created_at"`
}

const identityColumns = `id, display_name, coalesce(email, ''), kind, status, coalesce(manager_id::text, ''),
	coalesce(source_id::text, ''), coalesce(external_id, ''), coalesce(to_char(start_date, 'YYYY-MM-DD'), ''), coalesce(to_char(end_date, 'YYYY-MM-DD'), ''),
	attributes, created_at`

func scanIdentity(row pgx.CollectableRow) (identity, error) {
	var i identity
	err := row.Scan(&i.ID, &i.DisplayName, &i.Email, &i.Kind, &i.Status, &i.ManagerID,
		&i.SourceID, &i.ExternalID, &i.StartDate, &i.EndDate, &i.Attributes, &i.CreatedAt)
	return i, err
}

func (a *API) getIdentity(w http.ResponseWriter, r *http.Request) {
	var out identity
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := authz.LoadSubject(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadIdentity, Resource: authz.IdentityResource(target)}); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+identityColumns+` FROM identities WHERE id = $1`, target.ID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, scanIdentity)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// identityStatuses are the values identities.status allows.
var identityStatuses = map[string]bool{"pending": true, "active": true, "suspended": true, "terminated": true}

// listIdentities pages through all identities by id: ?limit=50&after=<id>.
// ?q= keeps the ones whose name or email contains it, ignoring case, and
// ?status= the ones in that status.
func (a *API) listIdentities(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	after := q.Get("after")
	if after != "" && !authz.ValidID(after) {
		writeError(w, http.StatusBadRequest, "after must be an identity id")
		return
	}
	search := strings.TrimSpace(q.Get("q"))
	if len(search) > 200 {
		writeError(w, http.StatusBadRequest, "q must be at most 200 characters")
		return
	}
	status := q.Get("status")
	if status != "" && !identityStatuses[status] {
		writeError(w, http.StatusBadRequest, "status must be pending, active, suspended or terminated")
		return
	}
	var out []identity
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ListIdentities, Resource: authz.DirectoryResource()}); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(),
			`SELECT `+identityColumns+` FROM identities
			 WHERE ($1 = '' OR id > $1::uuid)
			   AND ($3 = '' OR strpos(lower(display_name), lower($3)) > 0 OR strpos(lower(coalesce(email, '')), lower($3)) > 0)
			   AND ($4 = '' OR status = $4)
			 ORDER BY id LIMIT $2`, after, limit, search, status)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, scanIdentity)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": nonNil(out)})
}

type auditEvent struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	// ActorName and TargetName are the display names of the identities the
	// actor and target point to, when they are identities.
	ActorName  string          `json:"actor_name,omitempty"`
	TargetName string          `json:"target_name,omitempty"`
	Details    json.RawMessage `json:"details"`
}

// listAuditEvents pages backwards from the newest event: ?limit=50&before=<id>.
// ?identity=<id> keeps the events that identity did or that were done to it.
func (a *API) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "before must be a positive event id")
			return
		}
		before = n
	}
	identityID := r.URL.Query().Get("identity")
	if identityID != "" && !db.ValidUUID(identityID) {
		writeError(w, http.StatusBadRequest, "identity must be an identity id")
		return
	}
	var out []auditEvent
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadAuditLog, Resource: authz.AuditLogResource()}); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(),
			`SELECT e.id, e.occurred_at, e.actor, e.action, coalesce(e.target_type, ''), coalesce(e.target_id, ''),
			        coalesce(ai.display_name, ''), coalesce(ti.display_name, ''), e.details
			 FROM audit_events e
			 LEFT JOIN identities ai ON e.actor = 'identity:' || ai.id::text
			 LEFT JOIN identities ti ON e.target_type = 'identity' AND e.target_id = ti.id::text
			 WHERE ($1 = 0 OR e.id < $1)
			   AND ($3 = '' OR e.actor = 'identity:' || $3 OR (e.target_type = 'identity' AND e.target_id = $3))
			 ORDER BY e.id DESC LIMIT $2`, before, limit, identityID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (auditEvent, error) {
			var e auditEvent
			err := row.Scan(&e.ID, &e.OccurredAt, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.ActorName, &e.TargetName, &e.Details)
			return e, err
		})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": nonNil(out)})
}

type platformRole struct {
	authz.PlatformRole
	Members []authz.Member `json:"members"`
}

// listPlatformRoles shows the roles the person may see, with who holds them.
func (a *API) listPlatformRoles(w http.ResponseWriter, r *http.Request) {
	var out []platformRole
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		members, err := authz.Members(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, role := range authz.PlatformRoles {
			if a.authz.Authorize(authz.Request{Principal: me, Action: authz.ReadPlatformRole, Resource: authz.RoleResource(role.Name)}).Allowed {
				pr := platformRole{PlatformRole: role, Members: []authz.Member{}}
				for _, m := range members {
					if m.Role == role.Name {
						pr.Members = append(pr.Members, m)
					}
				}
				out = append(out, pr)
			}
		}
		if len(out) == 0 {
			return a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadPlatformRole, Resource: authz.RoleResource(authz.RoleAdmin)})
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// grantPlatformRole gives a platform role, optionally until a given time:
// PUT with {"expires_at": "2026-12-31T00:00:00Z"} or an empty body.
func (a *API) grantPlatformRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "body must be JSON like {\"expires_at\": \"2026-12-31T00:00:00Z\"}")
			return
		}
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "expires_at must be in the future")
		return
	}
	role := r.PathValue("role")
	if !authz.IsPlatformRole(role) {
		a.fail(w, r, authz.ErrNotFound)
		return
	}
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := authz.LoadSubject(r.Context(), tx, r.PathValue("identity"))
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.GrantPlatformRole, Resource: authz.RoleResource(role), Target: &target}); err != nil {
			return err
		}
		if err := authz.Grant(r.Context(), tx, a.tenantID, role, target.ID, body.ExpiresAt); err != nil {
			return err
		}
		details := map[string]any{"role": role}
		if body.ExpiresAt != nil {
			details["expires_at"] = body.ExpiresAt.UTC()
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "platform_role.granted", "identity", target.ID, details)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) revokePlatformRole(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	if !authz.IsPlatformRole(role) {
		a.fail(w, r, authz.ErrNotFound)
		return
	}
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := authz.LoadSubject(r.Context(), tx, r.PathValue("identity"))
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.RevokePlatformRole, Resource: authz.RoleResource(role), Target: &target}); err != nil {
			return err
		}
		if err := authz.Revoke(r.Context(), tx, role, target.ID); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "platform_role.revoked", "identity", target.ID, map[string]any{"role": role})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
		return 0, false
	}
	return n, true
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
