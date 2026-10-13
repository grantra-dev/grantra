// Built-in console policies. Every request is denied unless a permit matches,
// and any matching forbid wins over every permit.

// Everyone can see their own identity.
@id("identity-read-self")
permit (principal, action == Action::"identity.read", resource)
when { resource == principal };

// Managers can see their direct reports. Being a manager is not assigned: it
// follows from people having you as their manager.
@id("identity-read-direct-reports")
permit (principal, action == Action::"identity.read", resource is Identity)
when { resource has manager && resource.manager == principal };

// Admins can do everything, subject to the forbids below.
@id("admin-all")
permit (principal in Role::"grantra-admin", action, resource);

// Helpdesk can look people up, but cannot grant anything.
@id("helpdesk-directory")
permit (
  principal in Role::"grantra-helpdesk",
  action in [Action::"identity.read", Action::"identity.list"],
  resource
);

// Auditors read everything and change nothing.
@id("auditor-read-all")
permit (principal in Role::"grantra-auditor", action in Action::"read", resource);

// Nobody grants a platform role to themselves, not even an admin.
@id("no-self-grant")
forbid (principal, action == Action::"platform_role.grant", resource)
when { context.target == principal };

// Only active identities act. Sessions already end when an identity stops
// being active; this keeps that true for any future way in.
@id("only-active-identities")
forbid (principal, action, resource)
unless { principal.status == "active" };
