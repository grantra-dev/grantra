// Package authz decides what each person may do in the Grantra console.
//
// Decisions come from Cedar policies (embedded from policies/*.cedar) evaluated
// with cedar-go. Two kinds of facts feed them:
//
//   - Platform roles (grantra-admin, grantra-helpdesk, grantra-auditor), which
//     are entitlements of the tenant's built-in "Grantra" application. Holding
//     one makes the identity a member of Role::"<name>" in Cedar.
//   - Relationships in the data, such as manager_id, which need no assignment:
//     you are someone's manager because the data says so.
package authz

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"

	"github.com/cedar-policy/cedar-go"
)

// Platform role names, as stored in the built-in application's entitlements.
const (
	RoleAdmin    = "grantra-admin"
	RoleHelpdesk = "grantra-helpdesk"
	RoleAuditor  = "grantra-auditor"
)

// PlatformRole describes one platform role.
type PlatformRole struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

// PlatformRoles lists the platform roles every tenant gets.
var PlatformRoles = []PlatformRole{
	{RoleAdmin, "Grantra admin", "Configures Grantra: connectors, policies, workflows, campaigns and platform roles.", "critical"},
	{RoleHelpdesk, "Grantra helpdesk", "Looks people up and helps them, without granting any access.", "medium"},
	{RoleAuditor, "Grantra auditor", "Reads everything, including the audit log, and changes nothing.", "high"},
}

// IsPlatformRole reports whether name is a platform role.
func IsPlatformRole(name string) bool {
	return slices.ContainsFunc(PlatformRoles, func(r PlatformRole) bool { return r.Name == name })
}

// Action is something a person tries to do in the console.
type Action string

// Console actions. Each belongs to the "read" or the "write" action group.
const (
	ReadIdentity       Action = "identity.read"
	ListIdentities     Action = "identity.list"
	ReadAuditLog       Action = "audit.read"
	ReadPlatformRole   Action = "platform_role.read"
	GrantPlatformRole  Action = "platform_role.grant"
	RevokePlatformRole Action = "platform_role.revoke"
	ReadSources        Action = "identity_source.read"
	ManageSources      Action = "identity_source.manage"
	SyncSource         Action = "identity_source.sync"
	ReadLifecycle      Action = "lifecycle_event.read"
	ReadCatalog        Action = "catalog.read"
	ManageCatalog      Action = "catalog.manage"
	ReadAccessRules    Action = "access_rule.read"
	ManageAccessRules  Action = "access_rule.manage"
)

var actionGroup = map[Action]string{
	ReadIdentity:       "read",
	ListIdentities:     "read",
	ReadAuditLog:       "read",
	ReadPlatformRole:   "read",
	GrantPlatformRole:  "write",
	RevokePlatformRole: "write",
	ReadSources:        "read",
	ManageSources:      "write",
	SyncSource:         "write",
	ReadLifecycle:      "read",
	ReadCatalog:        "read",
	ManageCatalog:      "write",
	ReadAccessRules:    "read",
	ManageAccessRules:  "write",
}

// Writes reports whether the action changes something.
func (a Action) Writes() bool { return actionGroup[a] == "write" }

// Subject is an identity as policies see it.
type Subject struct {
	ID        string
	Status    string
	Kind      string
	ManagerID string
	// Roles are the platform roles the identity holds right now.
	Roles []string
}

func (s Subject) uid() cedar.EntityUID { return cedar.NewEntityUID("Identity", cedar.String(s.ID)) }

func (s Subject) entity() cedar.Entity {
	attrs := cedar.RecordMap{
		"status": cedar.String(s.Status),
		"kind":   cedar.String(s.Kind),
	}
	if s.ManagerID != "" {
		attrs["manager"] = cedar.NewEntityUID("Identity", cedar.String(s.ManagerID))
	}
	parents := make([]cedar.EntityUID, 0, len(s.Roles))
	for _, r := range s.Roles {
		parents = append(parents, cedar.NewEntityUID("Role", cedar.String(r)))
	}
	return cedar.Entity{
		UID:        s.uid(),
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: cedar.NewRecord(attrs),
	}
}

// Resource is what an action applies to.
type Resource struct {
	uid    cedar.EntityUID
	entity *cedar.Entity
}

// IdentityResource is an identity being looked at or changed.
func IdentityResource(s Subject) Resource {
	e := s.entity()
	return Resource{uid: e.UID, entity: &e}
}

// DirectoryResource is the list of all identities.
func DirectoryResource() Resource {
	return Resource{uid: cedar.NewEntityUID("Directory", "identities")}
}

// AuditLogResource is the tenant's audit log.
func AuditLogResource() Resource { return Resource{uid: cedar.NewEntityUID("AuditLog", "tenant")} }

// LifecycleResource is the tenant's identity sources (HR) and the lifecycle
// events they produce.
func LifecycleResource() Resource { return Resource{uid: cedar.NewEntityUID("Lifecycle", "tenant")} }

// CatalogResource is the tenant's catalog of applications, entitlements and
// business roles, with the birthright rules that hand them out.
func CatalogResource() Resource { return Resource{uid: cedar.NewEntityUID("Catalog", "tenant")} }

// RoleResource is a platform role.
func RoleResource(name string) Resource {
	return Resource{uid: cedar.NewEntityUID("Role", cedar.String(name))}
}

// Request asks whether Principal may perform Action on Resource. Target is the
// identity a platform role is granted to or revoked from.
type Request struct {
	Principal Subject
	Action    Action
	Resource  Resource
	Target    *Subject
}

// Decision is the outcome of a request.
type Decision struct {
	Allowed bool
	// Policies are the ids of the policies that decided: the permits that
	// allowed it, or the forbids that denied it. Empty when nothing permitted.
	Policies []string
	// Errors are policies that failed to evaluate; Cedar skips them.
	Errors []string
}

//go:embed policies/*.cedar
var policyFiles embed.FS

// Authorizer evaluates requests against the built-in policies.
type Authorizer struct {
	policies *cedar.PolicySet
}

// New loads the built-in policies. Every policy must carry a unique @id.
func New() (*Authorizer, error) {
	names, err := fs.Glob(policyFiles, "policies/*.cedar")
	if err != nil {
		return nil, err
	}
	set := cedar.NewPolicySet()
	for _, name := range names {
		src, err := policyFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		list, err := cedar.NewPolicyListFromBytes(name, src)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, p := range list {
			id, ok := p.Annotations()["id"]
			if !ok || id == "" {
				return nil, fmt.Errorf("%s:%d: policy without @id", name, p.Position().Line)
			}
			if !set.Add(cedar.PolicyID(id), p) {
				return nil, fmt.Errorf("%s: duplicate policy id %q", name, id)
			}
		}
	}
	return &Authorizer{policies: set}, nil
}

// Authorize evaluates req.
func (a *Authorizer) Authorize(req Request) Decision {
	entities := cedar.EntityMap{}
	for _, g := range []string{"read", "write"} {
		uid := cedar.NewEntityUID("Action", cedar.String(g))
		entities[uid] = cedar.Entity{UID: uid}
	}
	action := cedar.NewEntityUID("Action", cedar.String(req.Action))
	var groups []cedar.EntityUID
	if g, ok := actionGroup[req.Action]; ok {
		groups = append(groups, cedar.NewEntityUID("Action", cedar.String(g)))
	}
	entities[action] = cedar.Entity{UID: action, Parents: cedar.NewEntityUIDSet(groups...)}

	ctx := cedar.RecordMap{}
	if req.Target != nil {
		entities[req.Target.uid()] = req.Target.entity()
		ctx["target"] = req.Target.uid()
	}
	if req.Resource.entity != nil {
		entities[req.Resource.uid] = *req.Resource.entity
	}
	// The principal goes last: when it is also the resource or the target, its
	// roles must not be lost to a copy loaded without them.
	entities[req.Principal.uid()] = req.Principal.entity()

	decision, diag := a.policies.IsAuthorized(entities, cedar.Request{
		Principal: req.Principal.uid(),
		Action:    action,
		Resource:  req.Resource.uid,
		Context:   cedar.NewRecord(ctx),
	})
	d := Decision{Allowed: decision == cedar.Allow}
	for _, r := range diag.Reasons {
		d.Policies = append(d.Policies, string(r.PolicyID))
	}
	for _, e := range diag.Errors {
		d.Errors = append(d.Errors, fmt.Sprintf("%s: %s", e.PolicyID, e.Message))
	}
	return d
}
