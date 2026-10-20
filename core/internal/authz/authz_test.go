package authz

import (
	"os"
	"slices"
	"testing"

	internalast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

func TestPoliciesMatchSchema(t *testing.T) {
	src, err := os.ReadFile("policies/schema.cedarschema")
	if err != nil {
		t.Fatal(err)
	}
	var s schema.Schema
	s.SetFilename("schema.cedarschema")
	if err := s.UnmarshalCedar(src); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	v := validate.New(resolved)

	a := mustNew(t)
	for id, p := range a.policies.All() {
		if err := v.Policy(string(id), (*internalast.Policy)(p.AST())); err != nil {
			t.Errorf("policy %s: %v", id, err)
		}
	}
	// Every action the code uses is declared, in the group the code says.
	for act := range actionGroup {
		found := false
		for uid := range resolved.Actions {
			if string(uid.ID) == string(act) {
				found = true
			}
		}
		if !found {
			t.Errorf("action %s is not in the schema", act)
		}
	}
}

func mustNew(t *testing.T) *Authorizer {
	t.Helper()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDecisions(t *testing.T) {
	a := mustNew(t)

	admin := Subject{ID: "admin", Status: "active", Kind: "human", Roles: []string{RoleAdmin}}
	helpdesk := Subject{ID: "help", Status: "active", Kind: "human", Roles: []string{RoleHelpdesk}}
	auditor := Subject{ID: "audit", Status: "active", Kind: "human", Roles: []string{RoleAuditor}}
	manager := Subject{ID: "boss", Status: "active", Kind: "human"}
	report := Subject{ID: "ana", Status: "active", Kind: "human", ManagerID: "boss"}
	peer := Subject{ID: "luis", Status: "active", Kind: "human", ManagerID: "someone-else"}
	owner := Subject{ID: "owner", Status: "active", Kind: "human"}
	req := AccessRequestResource("r1", report.ID, report.ID, []string{manager.ID, owner.ID}, []string{manager.ID})
	adminReq := AccessRequestResource("r2", admin.ID, admin.ID, nil, nil)
	item := ReviewItemResource("i1", report.ID, []string{manager.ID})
	adminItem := ReviewItemResource("i2", admin.ID, nil)
	suspendedAdmin := Subject{ID: "gone", Status: "suspended", Kind: "human", Roles: []string{RoleAdmin}}

	tests := []struct {
		name     string
		req      Request
		want     bool
		policies []string
	}{
		// Reading identities.
		{"self", Request{Principal: report, Action: ReadIdentity, Resource: IdentityResource(report)}, true, []string{"identity-read-self"}},
		{"manager reads report", Request{Principal: manager, Action: ReadIdentity, Resource: IdentityResource(report)}, true, []string{"identity-read-direct-reports"}},
		{"report cannot read manager", Request{Principal: report, Action: ReadIdentity, Resource: IdentityResource(manager)}, false, nil},
		{"peer cannot read peer", Request{Principal: peer, Action: ReadIdentity, Resource: IdentityResource(report)}, false, nil},
		{"helpdesk reads anyone", Request{Principal: helpdesk, Action: ReadIdentity, Resource: IdentityResource(report)}, true, []string{"helpdesk-directory"}},
		{"auditor reads anyone", Request{Principal: auditor, Action: ReadIdentity, Resource: IdentityResource(report)}, true, []string{"auditor-read-all"}},

		// Listing everyone.
		{"employee cannot list", Request{Principal: report, Action: ListIdentities, Resource: DirectoryResource()}, false, nil},
		{"manager cannot list", Request{Principal: manager, Action: ListIdentities, Resource: DirectoryResource()}, false, nil},
		{"helpdesk lists", Request{Principal: helpdesk, Action: ListIdentities, Resource: DirectoryResource()}, true, nil},
		{"admin lists", Request{Principal: admin, Action: ListIdentities, Resource: DirectoryResource()}, true, []string{"admin-all"}},
		{"helpdesk cannot manage non-human identities", Request{Principal: helpdesk, Action: ManageIdentities, Resource: DirectoryResource()}, false, nil},
		{"auditor cannot manage non-human identities", Request{Principal: auditor, Action: ManageIdentities, Resource: DirectoryResource()}, false, nil},
		{"admin manages non-human identities", Request{Principal: admin, Action: ManageIdentities, Resource: DirectoryResource()}, true, []string{"admin-all"}},

		// Audit log.
		{"auditor reads audit log", Request{Principal: auditor, Action: ReadAuditLog, Resource: AuditLogResource()}, true, nil},
		{"admin reads audit log", Request{Principal: admin, Action: ReadAuditLog, Resource: AuditLogResource()}, true, nil},
		{"helpdesk cannot read audit log", Request{Principal: helpdesk, Action: ReadAuditLog, Resource: AuditLogResource()}, false, nil},
		{"employee cannot read audit log", Request{Principal: report, Action: ReadAuditLog, Resource: AuditLogResource()}, false, nil},

		// Platform roles.
		{"auditor sees role members", Request{Principal: auditor, Action: ReadPlatformRole, Resource: RoleResource(RoleAdmin)}, true, nil},
		{"helpdesk does not see role members", Request{Principal: helpdesk, Action: ReadPlatformRole, Resource: RoleResource(RoleAdmin)}, false, nil},
		{"admin grants to someone else", Request{Principal: admin, Action: GrantPlatformRole, Resource: RoleResource(RoleAuditor), Target: &report}, true, []string{"admin-all"}},
		{"admin cannot grant to self", Request{Principal: admin, Action: GrantPlatformRole, Resource: RoleResource(RoleAuditor), Target: &admin}, false, []string{"no-self-grant"}},
		{"admin revokes own role", Request{Principal: admin, Action: RevokePlatformRole, Resource: RoleResource(RoleAdmin), Target: &admin}, true, nil},
		{"auditor cannot grant", Request{Principal: auditor, Action: GrantPlatformRole, Resource: RoleResource(RoleAuditor), Target: &report}, false, nil},
		{"helpdesk cannot grant", Request{Principal: helpdesk, Action: GrantPlatformRole, Resource: RoleResource(RoleHelpdesk), Target: &report}, false, nil},
		{"manager cannot grant", Request{Principal: manager, Action: GrantPlatformRole, Resource: RoleResource(RoleAdmin), Target: &report}, false, nil},

		// Identity sources and lifecycle events.
		{"admin syncs a source", Request{Principal: admin, Action: SyncSource, Resource: LifecycleResource()}, true, []string{"admin-all"}},
		{"admin manages sources", Request{Principal: admin, Action: ManageSources, Resource: LifecycleResource()}, true, nil},
		{"auditor reads sources", Request{Principal: auditor, Action: ReadSources, Resource: LifecycleResource()}, true, []string{"auditor-read-all"}},
		{"auditor reads lifecycle events", Request{Principal: auditor, Action: ReadLifecycle, Resource: LifecycleResource()}, true, nil},
		{"auditor cannot sync", Request{Principal: auditor, Action: SyncSource, Resource: LifecycleResource()}, false, nil},
		{"helpdesk cannot read sources", Request{Principal: helpdesk, Action: ReadSources, Resource: LifecycleResource()}, false, nil},
		{"helpdesk cannot sync", Request{Principal: helpdesk, Action: SyncSource, Resource: LifecycleResource()}, false, nil},
		{"employee cannot sync", Request{Principal: report, Action: SyncSource, Resource: LifecycleResource()}, false, nil},
		{"admin manages service tokens", Request{Principal: admin, Action: ManageServiceTokens, Resource: ServiceTokensResource()}, true, []string{"admin-all"}},
		{"auditor reads service tokens", Request{Principal: auditor, Action: ReadServiceTokens, Resource: ServiceTokensResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot make service tokens", Request{Principal: auditor, Action: ManageServiceTokens, Resource: ServiceTokensResource()}, false, nil},
		{"admin turns modules off", Request{Principal: admin, Action: ManageModules, Resource: ModulesResource()}, true, []string{"admin-all"}},
		{"auditor reads modules", Request{Principal: auditor, Action: ReadModules, Resource: ModulesResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot turn modules off", Request{Principal: auditor, Action: ManageModules, Resource: ModulesResource()}, false, nil},
		{"helpdesk cannot turn modules off", Request{Principal: helpdesk, Action: ManageModules, Resource: ModulesResource()}, false, nil},
		{"admin changes settings", Request{Principal: admin, Action: ManageSettings, Resource: SettingsResource()}, true, []string{"admin-all"}},
		{"auditor reads settings", Request{Principal: auditor, Action: ReadSettings, Resource: SettingsResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot change settings", Request{Principal: auditor, Action: ManageSettings, Resource: SettingsResource()}, false, nil},
		{"helpdesk cannot read settings", Request{Principal: helpdesk, Action: ReadSettings, Resource: SettingsResource()}, false, nil},
		{"employee cannot change settings", Request{Principal: report, Action: ManageSettings, Resource: SettingsResource()}, false, nil},
		{"helpdesk cannot read service tokens", Request{Principal: helpdesk, Action: ReadServiceTokens, Resource: ServiceTokensResource()}, false, nil},

		// Catalog and birthright rules.
		{"admin manages catalog", Request{Principal: admin, Action: ManageCatalog, Resource: CatalogResource()}, true, []string{"admin-all"}},
		{"admin manages rules", Request{Principal: admin, Action: ManageAccessRules, Resource: CatalogResource()}, true, nil},
		{"auditor reads catalog", Request{Principal: auditor, Action: ReadCatalog, Resource: CatalogResource()}, true, nil},
		{"auditor reads rules", Request{Principal: auditor, Action: ReadAccessRules, Resource: CatalogResource()}, true, nil},
		{"auditor cannot change rules", Request{Principal: auditor, Action: ManageAccessRules, Resource: CatalogResource()}, false, nil},
		{"helpdesk cannot change catalog", Request{Principal: helpdesk, Action: ManageCatalog, Resource: CatalogResource()}, false, nil},
		{"employee cannot change rules", Request{Principal: report, Action: ManageAccessRules, Resource: CatalogResource()}, false, nil},

		// Access requests.
		{"anyone reads requestable access", Request{Principal: peer, Action: ReadRequestable, Resource: CatalogResource()}, true, []string{"requestable-read-all"}},
		{"request for self", Request{Principal: report, Action: CreateRequest, Resource: IdentityResource(report)}, true, []string{"request-for-self"}},
		{"manager requests for report", Request{Principal: manager, Action: CreateRequest, Resource: IdentityResource(report)}, true, []string{"request-for-direct-reports"}},
		{"peer cannot request for peer", Request{Principal: peer, Action: CreateRequest, Resource: IdentityResource(report)}, false, nil},
		{"helpdesk cannot request for others", Request{Principal: helpdesk, Action: CreateRequest, Resource: IdentityResource(report)}, false, nil},
		{"requester reads request", Request{Principal: report, Action: ReadRequest, Resource: req}, true, []string{"request-read-involved"}},
		{"approver reads request", Request{Principal: owner, Action: ReadRequest, Resource: req}, true, nil},
		{"peer cannot read request", Request{Principal: peer, Action: ReadRequest, Resource: req}, false, nil},
		{"auditor reads request", Request{Principal: auditor, Action: ReadRequest, Resource: req}, true, []string{"auditor-read-all"}},
		{"auditor lists requests", Request{Principal: auditor, Action: ListRequests, Resource: RequestsResource()}, true, nil},
		{"employee cannot list requests", Request{Principal: report, Action: ListRequests, Resource: RequestsResource()}, false, nil},
		{"current approver decides", Request{Principal: manager, Action: DecideRequest, Resource: req}, true, []string{"request-decide-approver"}},
		{"later approver waits", Request{Principal: owner, Action: DecideRequest, Resource: req}, false, nil},
		{"admin decides any step", Request{Principal: admin, Action: DecideRequest, Resource: req}, true, []string{"admin-all"}},
		{"auditor cannot decide", Request{Principal: auditor, Action: DecideRequest, Resource: req}, false, nil},
		{"nobody approves own access", Request{Principal: admin, Action: DecideRequest, Resource: adminReq}, false, []string{"no-self-approval"}},
		{"requester cancels", Request{Principal: report, Action: CancelRequest, Resource: req}, true, []string{"request-cancel-own"}},
		{"approver cannot cancel", Request{Principal: owner, Action: CancelRequest, Resource: req}, false, nil},

		// Access reviews.
		{"admin runs campaigns", Request{Principal: admin, Action: ManageCampaigns, Resource: ReviewsResource()}, true, []string{"admin-all"}},
		{"auditor reads campaigns", Request{Principal: auditor, Action: ReadCampaigns, Resource: ReviewsResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot run campaigns", Request{Principal: auditor, Action: ManageCampaigns, Resource: ReviewsResource()}, false, nil},
		{"manager cannot run campaigns", Request{Principal: manager, Action: ManageCampaigns, Resource: ReviewsResource()}, false, nil},
		{"employee cannot read campaigns", Request{Principal: report, Action: ReadCampaigns, Resource: ReviewsResource()}, false, nil},
		{"reviewer decides", Request{Principal: manager, Action: DecideReview, Resource: item}, true, []string{"review-decide-reviewer"}},
		{"others cannot decide", Request{Principal: peer, Action: DecideReview, Resource: item}, false, nil},
		{"auditor cannot decide", Request{Principal: auditor, Action: DecideReview, Resource: item}, false, nil},
		{"admin decides any item", Request{Principal: admin, Action: DecideReview, Resource: item}, true, []string{"admin-all"}},
		{"nobody reviews own access", Request{Principal: admin, Action: DecideReview, Resource: adminItem}, false, []string{"no-self-review"}},

		// Separation of duties.
		{"admin manages SoD", Request{Principal: admin, Action: ManageSoD, Resource: SoDResource()}, true, []string{"admin-all"}},
		{"auditor reads SoD", Request{Principal: auditor, Action: ReadSoD, Resource: SoDResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot grant exceptions", Request{Principal: auditor, Action: ManageSoD, Resource: SoDResource()}, false, nil},
		{"employee cannot read SoD", Request{Principal: report, Action: ReadSoD, Resource: SoDResource()}, false, nil},

		// The vault: admins manage it and auditors read it, but only people
		// holding a vault account's access check it out.
		{"admin manages the vault", Request{Principal: admin, Action: ManageVault, Resource: VaultResource()}, true, []string{"admin-all"}},
		{"auditor reads the vault", Request{Principal: auditor, Action: ReadVault, Resource: VaultResource()}, true, []string{"auditor-read-all"}},
		{"auditor cannot manage the vault", Request{Principal: auditor, Action: ManageVault, Resource: VaultResource()}, false, nil},
		{"employee cannot read the vault", Request{Principal: report, Action: ReadVault, Resource: VaultResource()}, false, nil},
		{"holder checks out", Request{Principal: report, Action: CheckoutVault, Resource: VaultAccountResource("v1", []string{report.ID})}, true, []string{"vault-checkout-holders"}},
		{"non-holder cannot check out", Request{Principal: peer, Action: CheckoutVault, Resource: VaultAccountResource("v1", []string{report.ID})}, false, nil},
		{"admin without access cannot check out", Request{Principal: admin, Action: CheckoutVault, Resource: VaultAccountResource("v1", []string{report.ID})}, false, []string{"vault-checkout-holders-only"}},
		{"admin holding access checks out", Request{Principal: admin, Action: CheckoutVault, Resource: VaultAccountResource("v1", []string{admin.ID})}, true, nil},
		{"auditor cannot check out", Request{Principal: auditor, Action: CheckoutVault, Resource: VaultAccountResource("v1", nil)}, false, []string{"vault-checkout-holders-only"}},
		{"suspended holder cannot check out", Request{Principal: suspendedAdmin, Action: CheckoutVault, Resource: VaultAccountResource("v1", []string{suspendedAdmin.ID})}, false, []string{"only-active-identities"}},

		// Break-glass: only responders open it, admins included; nobody
		// reviews their own use.
		{"holder connects over SSH", Request{Principal: report, Action: ConnectSSH, Resource: SSHTargetResource("s1", []string{report.ID})}, true, []string{"ssh-connect-holders"}},
		{"admin without access cannot connect over SSH", Request{Principal: admin, Action: ConnectSSH, Resource: SSHTargetResource("s1", []string{report.ID})}, false, []string{"ssh-connect-holders-only"}},
		{"auditor cannot connect over SSH", Request{Principal: auditor, Action: ConnectSSH, Resource: SSHTargetResource("s1", nil)}, false, []string{"ssh-connect-holders-only"}},
		{"holder gets a gateway token", Request{Principal: report, Action: ConnectGateway, Resource: GatewayTargetResource("g1", []string{report.ID})}, true, []string{"gateway-connect-holders"}},
		{"admin without access gets no gateway token", Request{Principal: admin, Action: ConnectGateway, Resource: GatewayTargetResource("g1", []string{report.ID})}, false, []string{"gateway-connect-holders-only"}},
		{"auditor gets no gateway token", Request{Principal: auditor, Action: ConnectGateway, Resource: GatewayTargetResource("g1", nil)}, false, []string{"gateway-connect-holders-only"}},
		{"responder breaks glass", Request{Principal: report, Action: BreakGlassVault, Resource: BreakGlassResource("v1", []string{report.ID})}, true, []string{"vault-break-glass-responders"}},
		{"admin responder breaks glass", Request{Principal: admin, Action: BreakGlassVault, Resource: BreakGlassResource("v1", []string{admin.ID})}, true, nil},
		{"admin not a responder cannot break glass", Request{Principal: admin, Action: BreakGlassVault, Resource: BreakGlassResource("v1", []string{report.ID})}, false, []string{"vault-break-glass-responders-only"}},
		{"others cannot break glass", Request{Principal: peer, Action: BreakGlassVault, Resource: BreakGlassResource("v1", []string{report.ID})}, false, nil},
		{"admin reviews a break-glass use", Request{Principal: admin, Action: ReviewBreakGlass, Resource: BreakGlassUseResource("c1", report.ID)}, true, []string{"admin-all"}},
		{"nobody reviews own break-glass use", Request{Principal: admin, Action: ReviewBreakGlass, Resource: BreakGlassUseResource("c1", admin.ID)}, false, []string{"no-self-break-glass-review"}},
		{"auditor cannot review break-glass", Request{Principal: auditor, Action: ReviewBreakGlass, Resource: BreakGlassUseResource("c1", report.ID)}, false, nil},
		{"employee cannot review break-glass", Request{Principal: peer, Action: ReviewBreakGlass, Resource: BreakGlassUseResource("c1", report.ID)}, false, nil},

		// Just-in-time access: admins make others eligible, never
		// themselves; people activate only their own eligibilities.
		{"admin makes someone eligible", Request{Principal: admin, Action: ManageAccess, Resource: IdentityResource(report)}, true, []string{"admin-all"}},
		{"admin cannot make self eligible", Request{Principal: admin, Action: ManageAccess, Resource: IdentityResource(admin)}, false, []string{"no-self-eligibility"}},
		{"manager cannot make reports eligible", Request{Principal: manager, Action: ManageAccess, Resource: IdentityResource(report)}, false, nil},
		{"auditor cannot make people eligible", Request{Principal: auditor, Action: ManageAccess, Resource: IdentityResource(report)}, false, nil},
		{"person activates own", Request{Principal: report, Action: ActivateAccess, Resource: IdentityResource(report)}, true, []string{"activate-own"}},
		{"admin cannot activate for others", Request{Principal: admin, Action: ActivateAccess, Resource: IdentityResource(report)}, false, []string{"activate-own-only"}},
		{"manager cannot activate for reports", Request{Principal: manager, Action: ActivateAccess, Resource: IdentityResource(report)}, false, nil},
		{"suspended person cannot activate", Request{Principal: suspendedAdmin, Action: ActivateAccess, Resource: IdentityResource(suspendedAdmin)}, false, []string{"only-active-identities"}},

		// Inactive identities.
		{"suspended admin does nothing", Request{Principal: suspendedAdmin, Action: ListIdentities, Resource: DirectoryResource()}, false, []string{"only-active-identities"}},
		{"suspended person cannot read self", Request{Principal: suspendedAdmin, Action: ReadIdentity, Resource: IdentityResource(suspendedAdmin)}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := a.Authorize(tt.req)
			if len(d.Errors) > 0 {
				t.Fatalf("evaluation errors: %v", d.Errors)
			}
			if d.Allowed != tt.want {
				t.Fatalf("allowed = %v (policies %v), want %v", d.Allowed, d.Policies, tt.want)
			}
			for _, p := range tt.policies {
				if !slices.Contains(d.Policies, p) {
					t.Errorf("deciding policies = %v, want %s among them", d.Policies, p)
				}
			}
		})
	}
}

func TestSelfReadKeepsPrincipalRoles(t *testing.T) {
	// The resource copy of the principal carries no roles; the decision must
	// still see the principal's own.
	a := mustNew(t)
	auditor := Subject{ID: "audit", Status: "active", Kind: "human", Roles: []string{RoleAuditor}}
	bare := auditor
	bare.Roles = nil
	d := a.Authorize(Request{Principal: auditor, Action: ReadIdentity, Resource: IdentityResource(bare)})
	if !d.Allowed || !slices.Contains(d.Policies, "auditor-read-all") {
		t.Fatalf("decision = %+v, want allowed by auditor-read-all too", d)
	}
}

func TestIsPlatformRole(t *testing.T) {
	for _, r := range []string{RoleAdmin, RoleHelpdesk, RoleAuditor} {
		if !IsPlatformRole(r) {
			t.Errorf("IsPlatformRole(%q) = false", r)
		}
	}
	if IsPlatformRole("grantra-root") {
		t.Error(`IsPlatformRole("grantra-root") = true`)
	}
}
