// Package console serves the JSON API behind the Grantra console. Every
// endpoint needs a signed-in person, and every decision about what that
// person may see or change goes through the Cedar policies in package authz.
package console

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/gateways/cloud"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/secretstore"
	"github.com/grantra-dev/grantra/core/internal/settings"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// API holds the console endpoints for one tenant.
type API struct {
	pool     *pgxpool.Pool
	tenantID string
	authz    *authz.Authorizer
	log      *slog.Logger

	// Secrets keeps the connector secrets typed in the console and resolves
	// every secret reference in connector configs.
	Secrets *secretstore.Store

	// EmailEnabled says whether notification email is set up, so the
	// console can tell people when their settings have no effect.
	EmailEnabled bool
	// Vault keeps privileged credentials; it cannot store or reveal secrets
	// until it has a key.
	Vault *vault.Vault
	// SSH signs certificates for SSH targets; GatewayAddress is where people
	// reach the SSH gateway, shown with the command to connect.
	SSH            *sshaccess.Service
	GatewayAddress string
	// Gateways issues tokens for the clusters and databases behind the
	// gateway; GatewayAddresses is where people reach its listeners.
	Gateways         *gateways.Service
	GatewayAddresses GatewayAddresses
	// Desktops runs desktop sessions, when guacd is set up; Consoles signs
	// people in to cloud consoles.
	Desktops Desktops
	Consoles *cloud.Consoles
}

// New returns the console API for the given tenant.
func New(pool *pgxpool.Pool, tenantID string, az *authz.Authorizer, log *slog.Logger) *API {
	return &API{pool: pool, tenantID: tenantID, authz: az, log: log, Secrets: &secretstore.Store{Pool: pool, TenantID: tenantID},
		Vault: vault.New(nil, connector.EnvSecrets), SSH: sshaccess.New(nil), Gateways: gateways.New(nil)}
}

// Register adds the console routes to mux. require must reject requests that
// have no signed-in person and store the person with auth.ContextWithPrincipal.
func (a *API) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	route := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, require(sameOrigin(h)))
	}
	// Uploads that scripts make with a service token instead of a session.
	tokenRoute := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, a.sessionOrToken(require, sameOrigin(h)))
	}
	route("GET /api/v1/me", a.me)
	route("GET /api/v1/me/inbox", a.getInbox)
	route("GET /api/v1/me/home", a.getHome)
	route("GET /api/v1/search", a.search)
	route("GET /api/v1/modules", a.listModules)
	route("PUT /api/v1/modules/{module}", a.setModule)
	route("GET /api/v1/settings", a.listSettings)
	route("PUT /api/v1/settings/{key}", a.saveSetting)
	route("POST /api/v1/setting-changes/{id}/decision", a.decideSettingChange)
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
	route("PUT /api/v1/identity-sources/{id}", a.updateSource)
	route("GET /api/v1/identity-sources/{id}/runs", a.listRuns)
	tokenRoute("POST /api/v1/identity-sources/{id}/runs", a.syncSource)
	tokenRoute("POST /api/v1/identity-sources/{id}/pulls", a.pullSource)
	route("GET /api/v1/lifecycle-events", a.listLifecycleEvents)
	route("GET /api/v1/identities/{id}/access", a.identityAccess)
	route("GET /api/v1/applications", a.listApplications)
	route("POST /api/v1/applications", a.createApplication)
	route("PUT /api/v1/applications/{id}", a.updateApplication)
	route("DELETE /api/v1/applications/{id}", a.deleteApplication)
	route("PUT /api/v1/applications/{id}/retired", a.retireApplication)
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
	route("POST /api/v1/connector-secrets", a.createConnectorSecret)
	route("POST /api/v1/applications/{id}/aggregations", a.aggregate)
	tokenRoute("POST /api/v1/applications/{id}/account-imports", a.importAccounts)
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
	route("GET /api/v1/approval-policies", a.listApprovalPolicies)
	route("POST /api/v1/approval-policies", a.saveApprovalPolicy)
	route("PUT /api/v1/approval-policies/{id}", a.saveApprovalPolicy)
	route("DELETE /api/v1/approval-policies/{id}", a.deleteApprovalPolicy)
	route("PUT /api/v1/approval-policies/order", a.reorderApprovalPolicies)
	route("POST /api/v1/approval-policies/simulate", a.simulateApprovals)
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
	route("GET /api/v1/vault/accounts", a.listVaultAccounts)
	route("POST /api/v1/vault/accounts", a.createVaultAccount)
	route("GET /api/v1/vault/accounts/{id}", a.getVaultAccount)
	route("PUT /api/v1/vault/accounts/{id}", a.updateVaultAccount)
	route("DELETE /api/v1/vault/accounts/{id}", a.deleteVaultAccount)
	route("PUT /api/v1/vault/accounts/{id}/secret", a.setVaultSecret)
	route("POST /api/v1/vault/accounts/{id}/rotations", a.rotateVaultAccount)
	route("POST /api/v1/vault/accounts/{id}/checkouts", a.checkoutVaultAccount)
	route("POST /api/v1/vault/checkouts/{id}/checkin", a.checkinVault)
	route("GET /api/v1/vault/discovery", a.discoverVaultAccounts)
	route("POST /api/v1/vault/accounts/{id}/break-glass", a.breakGlass)
	route("GET /api/v1/vault/break-glass", a.listBreakGlassUses)
	route("POST /api/v1/vault/break-glass/{id}/review", a.reviewBreakGlass)
	route("GET /api/v1/ssh", a.getSSH)
	route("GET /api/v1/ssh/targets", a.listSSHTargets)
	route("POST /api/v1/ssh/targets", a.createSSHTarget)
	route("PUT /api/v1/ssh/targets/{id}", a.updateSSHTarget)
	route("DELETE /api/v1/ssh/targets/{id}", a.deleteSSHTarget)
	route("POST /api/v1/ssh/targets/{id}/certificates", a.issueSSHCertificate)
	route("GET /api/v1/ssh/sessions", a.listSSHSessions)
	route("GET /api/v1/ssh/sessions/{id}", a.getSSHSession)
	route("GET /api/v1/ssh/sessions/{id}/recording", a.sshRecording)
	route("POST /api/v1/ssh/sessions/{id}/terminate", a.terminateSSHSession)
	route("GET /api/v1/ssh/commands", a.searchSSHCommands)
	route("GET /api/v1/gateway", a.getGateway)
	route("GET /api/v1/gateway/targets", a.listGatewayTargets)
	route("POST /api/v1/gateway/targets", a.createGatewayTarget)
	route("PUT /api/v1/gateway/targets/{id}", a.updateGatewayTarget)
	route("DELETE /api/v1/gateway/targets/{id}", a.deleteGatewayTarget)
	route("POST /api/v1/gateway/targets/{id}/tokens", a.issueGatewayToken)
	route("GET /api/v1/gateway/targets/{id}/rdp", a.connectDesktop)
	route("POST /api/v1/gateway/targets/{id}/console", a.openConsole)
	route("GET /api/v1/gateway/sessions", a.listGatewaySessions)
	route("GET /api/v1/gateway/sessions/{id}", a.getGatewaySession)
	route("GET /api/v1/gateway/sessions/{id}/recording", a.gatewayRecording)
	route("POST /api/v1/gateway/sessions/{id}/terminate", a.terminateGatewaySession)
	route("GET /api/v1/gateway/events", a.searchGatewayEvents)
	route("GET /api/v1/identities/{id}/eligibilities", a.identityEligibilities)
	route("POST /api/v1/identities/{id}/eligibilities", a.makeEligible)
	route("DELETE /api/v1/eligibilities/{id}", a.removeEligibility)
	route("POST /api/v1/eligibilities/{id}/activations", a.activateEligibility)
	route("POST /api/v1/eligibilities/{id}/deactivate", a.deactivateEligibility)
	route("PUT /api/v1/entitlements/{id}", a.updateEntitlement)
	route("DELETE /api/v1/entitlements/{id}", a.deleteEntitlement)
	route("PUT /api/v1/entitlements/{id}/jit", a.setEntitlementJIT)
	route("GET /api/v1/standing-access", a.standingAccess)
	route("GET /api/v1/service-tokens", a.listServiceTokens)
	route("POST /api/v1/service-tokens", a.createServiceToken)
	route("DELETE /api/v1/service-tokens/{id}", a.revokeServiceToken)
	route("POST /api/v1/assignments/{id}/eligibility", a.convertToEligibility)
	route("GET /api/v1/threats", a.getThreats)
	route("GET /api/v1/threats/alerts", a.listThreatAlerts)
	route("GET /api/v1/threats/alerts/{id}", a.getThreatAlert)
	route("POST /api/v1/threats/alerts/{id}/close", a.closeThreatAlert)
	route("GET /api/v1/threats/detections", a.listThreatDetections)
	route("PUT /api/v1/threats/detections/{id}", a.setThreatDetection)
	route("POST /api/v1/threats/scan", a.scanThreats)
	route("GET /api/v1/sign-in-events", a.listSignIns)
	tokenRoute("POST /api/v1/sign-in-events", a.receiveSignIns)
	route("GET /api/v1/identities/{id}/threat-responses", a.listThreatResponses)
	route("POST /api/v1/identities/{id}/threat-responses", a.respondToThreat)
	route("GET /api/v1/attack-paths", a.listAttackPaths)
	route("GET /api/v1/attack-paths/{id}", a.getAttackPaths)
	route("GET /api/v1/identities/{id}/reach", a.identityReach)
	route("GET /api/v1/non-human-identities", a.listNonHuman)
	route("POST /api/v1/non-human-identities", a.createNonHuman)
	route("GET /api/v1/non-human-identities/summary", a.nonHumanSummary)
	route("GET /api/v1/non-human-identities/candidates", a.listNonHumanCandidates)
	route("GET /api/v1/non-human-identities/{id}", a.getNonHuman)
	route("PATCH /api/v1/non-human-identities/{id}", a.updateNonHuman)
	route("POST /api/v1/non-human-identities/{id}/retire", a.retireNonHuman)
	route("GET /api/v1/unused-access", a.listUnusedAccess)
	route("GET /api/v1/role-suggestions", a.listRoleSuggestions)

	// Apps' backends ask with an API key, not a session.
	mux.HandleFunc("POST /api/v1/authz/check", a.authzCheck)
	// Servers fetch the key they trust while being set up; it is public.
	mux.HandleFunc("GET /api/v1/ssh/ca.pub", a.sshCAPublicKey(sshaccess.PeopleCA))
	mux.HandleFunc("GET /api/v1/ssh/gateway_ca.pub", a.sshCAPublicKey(sshaccess.GatewayCA))
	mux.HandleFunc("GET /api/v1/gateway/ca.pem", a.gatewayCAFile)
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
				httpjson.Error(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type me struct {
	auth.Principal
	PlatformRoles []string `json:"platform_roles"`
	// ManagesPeople and OwnsApplications come from the data, not from an
	// assignment, and decide which console sections the person sees.
	ManagesPeople    bool `json:"manages_people"`
	OwnsApplications bool `json:"owns_applications"`
	// DisabledModules are the parts of Grantra the tenant turned off, whose
	// pages the console hides.
	DisabledModules []string `json:"disabled_modules"`
	// Settings are the tenant's settings everyone may see, by key, for the
	// console to explain what it shows (such as how long access goes
	// unused before it counts as unused).
	Settings map[string]int `json:"settings"`
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	out := me{Principal: p}
	err := a.inTenant(r, func(tx pgx.Tx, s authz.Subject) error {
		out.PlatformRoles = httpjson.NonNil(s.Roles)
		if err := tx.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM identities WHERE manager_id = $1 AND status <> 'terminated'),
			        EXISTS (SELECT 1 FROM applications WHERE owner_id = $1)`, s.ID).
			Scan(&out.ManagesPeople, &out.OwnsApplications); err != nil {
			return err
		}
		off, err := disabledModules(r.Context(), tx)
		if err != nil {
			return err
		}
		out.DisabledModules = httpjson.NonNil(off)
		out.Settings, err = settings.Public(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
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
	SourceID   string `json:"source_id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	StartDate  string `json:"start_date,omitempty"`
	EndDate    string `json:"end_date,omitempty"`
	// Purpose and SpiffeID are for non-human identities, whose manager is
	// their owner.
	Purpose    string         `json:"purpose,omitempty"`
	SpiffeID   string         `json:"spiffe_id,omitempty"`
	Attributes map[string]any `json:"attributes"`
	CreatedAt  time.Time      `json:"created_at"`
}

const identityColumns = `id, display_name, coalesce(email, ''), kind, status, coalesce(manager_id::text, ''),
	coalesce(source_id::text, ''), coalesce(external_id, ''), coalesce(to_char(start_date, 'YYYY-MM-DD'), ''), coalesce(to_char(end_date, 'YYYY-MM-DD'), ''),
	coalesce(purpose, ''), coalesce(spiffe_id, ''), attributes, created_at`

func scanIdentity(row pgx.CollectableRow) (identity, error) {
	var i identity
	err := row.Scan(&i.ID, &i.DisplayName, &i.Email, &i.Kind, &i.Status, &i.ManagerID,
		&i.SourceID, &i.ExternalID, &i.StartDate, &i.EndDate, &i.Purpose, &i.SpiffeID, &i.Attributes, &i.CreatedAt)
	return i, err
}

func (a *API) getIdentity(w http.ResponseWriter, r *http.Request) {
	var out identity
	err := a.guardedOnIdentity(r, authz.ReadIdentity, func(tx pgx.Tx, _, target authz.Subject) error {
		rows, err := tx.Query(r.Context(), `SELECT `+identityColumns+` FROM identities WHERE id = $1`, target.ID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, scanIdentity)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// identityStatuses are the values identities.status allows.
var identityStatuses = map[string]bool{"pending": true, "active": true, "suspended": true, "terminated": true}

// listIdentities pages through all identities by id: ?limit=50&after=<id>.
// ?q= keeps the ones whose name or email contains it, ignoring case, and
// ?status= the ones in that status, and ?kind=human or ?kind=non_human
// people or the service accounts, agents and workloads.
func (a *API) listIdentities(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	after := q.Get("after")
	if after != "" && !authz.ValidID(after) {
		httpjson.Error(w, http.StatusBadRequest, "after must be an identity id")
		return
	}
	search := strings.TrimSpace(q.Get("q"))
	if len(search) > 200 {
		httpjson.Error(w, http.StatusBadRequest, "q must be at most 200 characters")
		return
	}
	status := q.Get("status")
	if status != "" && !identityStatuses[status] {
		httpjson.Error(w, http.StatusBadRequest, "status must be pending, active, suspended or terminated")
		return
	}
	kind := q.Get("kind")
	if kind != "" && kind != "human" && kind != "non_human" {
		httpjson.Error(w, http.StatusBadRequest, "kind must be human or non_human")
		return
	}
	var out []identity
	err := a.guardedOn(r, authz.ListIdentities, authz.DirectoryResource(), func(tx pgx.Tx, _ authz.Subject) error {
		rows, err := tx.Query(r.Context(),
			`SELECT `+identityColumns+` FROM identities
			 WHERE ($1 = '' OR id > $1::uuid)
			   AND ($3 = '' OR lower(display_name) LIKE $5 OR lower(email) LIKE $5)
			   AND ($4 = '' OR status = $4)
			   AND ($6 = '' OR (kind = 'human') = ($6 = 'human'))
			 ORDER BY id LIMIT $2`, after, limit, search, status, db.ContainsPattern(search), kind)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, scanIdentity)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"identities": httpjson.NonNil(out)}, err)
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

// uuidRegexp matches a row id, for SQL that must not cast anything else.
const uuidRegexp = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

// listAuditEvents pages backwards from the newest event: ?limit=50&before=<id>.
// ?identity=<id> keeps the events that identity did or that were done to it.
func (a *API) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	before, ok := beforeCursor(w, r)
	if !ok {
		return
	}
	identityID, ok := identityParam(w, r)
	if !ok {
		return
	}
	var out []auditEvent
	err := a.guardedOn(r, authz.ReadAuditLog, authz.AuditLogResource(), func(tx pgx.Tx, _ authz.Subject) error {
		// With ?identity= the page is the newest of what they did and what
		// was done to them, each read from its own index.
		rows, err := tx.Query(r.Context(),
			`WITH page AS (
			   (SELECT * FROM audit_events WHERE $3 = '' AND ($1 = 0 OR id < $1) ORDER BY id DESC LIMIT $2)
			   UNION ALL
			   (SELECT * FROM audit_events WHERE $3 <> '' AND actor = 'identity:' || $3 AND ($1 = 0 OR id < $1)
			    ORDER BY id DESC LIMIT $2)
			   UNION ALL
			   (SELECT * FROM audit_events WHERE $3 <> '' AND target_type = 'identity' AND target_id = $3
			      AND actor <> 'identity:' || $3 AND ($1 = 0 OR id < $1)
			    ORDER BY id DESC LIMIT $2))
			 SELECT e.id, e.occurred_at, e.actor, e.action, coalesce(e.target_type, ''), coalesce(e.target_id, ''),
			        coalesce(ai.display_name, ''), coalesce(ti.display_name, ''), e.details
			 FROM page e
			 LEFT JOIN identities ai ON ai.id = CASE WHEN e.actor ~ '^identity:`+uuidRegexp+`$' THEN substr(e.actor, 10)::uuid END
			 LEFT JOIN identities ti ON ti.id = CASE WHEN e.target_type = 'identity' AND e.target_id ~ '^`+uuidRegexp+`$' THEN e.target_id::uuid END
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
	a.respond(w, r, http.StatusOK, map[string]any{"events": httpjson.NonNil(out)}, err)
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
			if a.may(me, authz.ReadPlatformRole, authz.RoleResource(role.Name)) {
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
	a.respond(w, r, http.StatusOK, map[string]any{"roles": out}, err)
}

// grantPlatformRole gives a platform role, optionally until a given time:
// PUT with {"expires_at": "2026-12-31T00:00:00Z"} or an empty body.
func (a *API) grantPlatformRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if r.ContentLength != 0 && !decodeBodyMax(w, r, &body, 4096, `{"expires_at": "2026-12-31T00:00:00Z"}`) {
		return
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now()) {
		httpjson.Error(w, http.StatusBadRequest, "expires_at must be in the future")
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
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "platform_role.granted", "identity", target.ID, details)
	})
	a.noContent(w, r, err)
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
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "platform_role.revoked", "identity", target.ID, map[string]any{"role": role})
	})
	a.noContent(w, r, err)
}
