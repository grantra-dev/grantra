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

// Everyone can see what they could ask for.
@id("requestable-read-all")
permit (principal, action == Action::"requestable.read", resource);

// People ask for access for themselves, and managers for their reports.
@id("request-for-self")
permit (principal, action == Action::"access_request.create", resource)
when { resource == principal };

@id("request-for-direct-reports")
permit (principal, action == Action::"access_request.create", resource is Identity)
when { resource has manager && resource.manager == principal };

// A request is visible to whoever asked, whoever would get the access and
// everyone asked to approve it.
@id("request-read-involved")
permit (principal, action == Action::"access_request.read", resource is AccessRequest)
when {
  resource.requester == principal ||
  resource.beneficiary == principal ||
  resource.approvers.contains(principal)
};

// The approver of the current step decides it. Admins can decide any step,
// including the ones that wait on an admin.
@id("request-decide-approver")
permit (principal, action == Action::"access_request.decide", resource is AccessRequest)
when { resource.current_approvers.contains(principal) };

@id("request-cancel-own")
permit (principal, action == Action::"access_request.cancel", resource is AccessRequest)
when { resource.requester == principal || resource.beneficiary == principal };

// Nobody approves their own access, not even an admin.
@id("no-self-approval")
forbid (principal, action == Action::"access_request.decide", resource is AccessRequest)
when { resource.beneficiary == principal };

// Reviewers decide the access they were asked to review. Admins can decide
// any item, including the ones with nobody else to ask.
@id("review-decide-reviewer")
permit (principal, action == Action::"review_item.decide", resource is ReviewItem)
when { resource.reviewers.contains(principal) };

// Nobody reviews their own access, not even an admin.
@id("no-self-review")
forbid (principal, action == Action::"review_item.decide", resource is ReviewItem)
when { resource.identity == principal };

// People check out the vault accounts they hold access to.
@id("vault-checkout-holders")
permit (principal, action == Action::"vault_account.checkout", resource is VaultAccount)
when { resource.holders.contains(principal) };

// Nobody reads a vault secret without holding access to it, not even an
// admin: admins request it like anyone else.
@id("vault-checkout-holders-only")
forbid (principal, action == Action::"vault_account.checkout", resource is VaultAccount)
unless { resource.holders.contains(principal) };

// Only active identities act. Sessions already end when an identity stops
// being active; this keeps that true for any future way in.
@id("only-active-identities")
forbid (principal, action, resource)
unless { principal.status == "active" };
