package reports

import (
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// Report ids, which frameworks' controls point to.
const (
	Reviews       = "reviews"
	Leavers       = "leavers"
	Requests      = "requests"
	SoD           = "sod"
	Privileged    = "privileged"
	Access        = "access"
	AccessHistory = "access_history"
	Admin         = "admin"
	Threats       = "threats"
)

// stillHeld is true when a review item's person still holds what it is
// about: an unexpired assignment, directly or through a business role.
func stillHeld(item string) string {
	return `CASE WHEN ` + item + `.entitlement_id IS NOT NULL THEN ` + catalog.HeldSQL(item+".identity_id", item+".entitlement_id") + `
	  ELSE EXISTS (SELECT 1 FROM access_assignments x WHERE x.identity_id = ` + item + `.identity_id AND x.role_id = ` + item + `.role_id
	               AND (x.expires_at IS NULL OR x.expires_at > now())) END`
}

// adminActions are the audit events that change how Grantra is set up, or
// that someone tried and was refused.
var adminActions = []string{
	"platform_role.granted", "platform_role.revoked", "setting.", "approval_policy.", "sod.policy_", "sod.exception_granted",
	"application.created", "application.updated", "application.deleted", "application.retired", "application.restored",
	"application.connector_set", "application.provisioning_set", "access_rule.created", "access_rule.updated",
	"access_rule.deleted", "role.", "entitlement.", "identity_source.created", "identity_source.updated",
	"connector_secret.", "service_token.created", "service_token.revoked", "module.", "login.", "itdr.detection_saved",
	"app_authz.", "report_framework.", "report_schedule.", "external_auditor.", "vault.account_", "vault.secret_set", "ssh.target_", "gateway.target_",
	"identity.created", "identity.owner_changed", "identity.retired", "authz.denied",
}

var definitions = []Definition{
	{
		ID:          Reviews,
		Title:       "Access reviews",
		Description: "Every campaign that ran in the period, who reviewed each access, what they decided and when, and whether revoked access is really gone.",
		Period:      true, Application: true,
		Tables: []Table{
			{
				Name: "campaigns", Title: "Campaigns",
				Description: "Campaigns that were open at some point of the period.",
				Columns: columns("campaign", "Campaign", "reviewer", "Reviewed by", "launched_by", "Launched by", "launched_at", "Launched (UTC)",
					"due_at", "Due (UTC)", "status", "Status", "closed_at", "Closed (UTC)", "unreviewed", "Access nobody reviewed",
					"items", "Access to review", "kept", "Kept", "revoked", "Revoked", "pending", "Not reviewed"),
				sql: `SELECT c.name, c.reviewer, ` + actor("c.created_by") + `, ` + ts("c.created_at") + `, ` + ts("c.due_at") + `,
				        c.status, ` + ts("c.closed_at") + `, CASE c.on_missing WHEN 'revoke' THEN 'revoked' ELSE 'kept' END,
				        count(ri.id)::text, count(ri.id) FILTER (WHERE ri.decision = 'keep')::text,
				        count(ri.id) FILTER (WHERE ri.decision = 'revoke')::text, count(ri.id) FILTER (WHERE ri.decision = 'pending')::text
				 FROM p CROSS JOIN review_campaigns c
				 LEFT JOIN review_items ri ON ri.campaign_id = c.id AND ` + inApplication("ri.role_id", "ri.entitlement_id") + `
				 WHERE ` + during("c.created_at", "c.closed_at") + `
				 GROUP BY c.id, p.app HAVING p.app IS NULL OR count(ri.id) > 0
				 ORDER BY c.created_at, c.name`,
			},
			{
				Name: "decisions", Title: "Decisions",
				Description: "Each access reviewed. Still held and still in the application are checked now, for revoked access.",
				Columns: columns("campaign", "Campaign", "person", "Person", "email", "Email", "access", "Access", "application", "Application",
					"risk", "Risk", "origin", "How it was given", "granted_at", "Held since (UTC)", "reviewer", "Reviewer",
					"decision", "Decision", "decided_by", "Decided by", "decided_at", "Decided (UTC)", "comment", "Comment",
					"still_held", "Revoked but still held", "still_in_application", "Revoked but still in the application"),
				sql: `SELECT c.name, ` + person("ri.identity_id") + `, ` + email("ri.identity_id") + `, ` + access("ri.role_id", "ri.entitlement_id") + `,
				        ` + application("ri.entitlement_id") + `, ` + risk("ri.entitlement_id") + `, ri.origin, ` + ts("ri.granted_at") + `,
				        CASE WHEN ri.reviewer_id IS NULL THEN 'any admin' ELSE ` + person("ri.reviewer_id") + ` END,
				        ri.decision, CASE WHEN ri.decided_by IS NULL THEN '' ELSE ` + actor("ri.decided_by") + ` END,
				        ` + ts("ri.decided_at") + `, coalesce(ri.comment, ''),
				        CASE WHEN ri.decision = 'revoke' THEN ` + yesNo(stillHeld("ri")) + ` ELSE '' END,
				        CASE WHEN ri.decision = 'revoke' AND ri.entitlement_id IS NOT NULL THEN ` + yesNo(`EXISTS (
				          SELECT 1 FROM account_entitlements ae JOIN accounts a ON a.id = ae.account_id
				          WHERE a.identity_id = ri.identity_id AND ae.entitlement_id = ri.entitlement_id AND a.status = 'active')`) + ` ELSE '' END
				 FROM p CROSS JOIN review_items ri JOIN review_campaigns c ON c.id = ri.campaign_id
				 WHERE ` + during("c.created_at", "c.closed_at") + ` AND ` + inApplication("ri.role_id", "ri.entitlement_id") + `
				 ORDER BY c.created_at, c.name, 2, 4`,
			},
		},
		facts: []fact{
			{label: "Campaigns", table: "campaigns"},
			{label: "Access reviewed", table: "decisions", column: "decision", values: []string{"keep", "revoke"}},
			{label: "Revoked", table: "decisions", column: "decision", values: []string{"revoke"}},
			{label: "Not reviewed", table: "decisions", column: "decision", values: []string{"pending"}},
			{label: "Revoked but still held", table: "decisions", column: "still_held", values: []string{"yes"}},
		},
	},
	{
		ID:          Leavers,
		Title:       "Leavers and orphan accounts",
		Description: "Everyone who left in the period, how long their accounts took to close against your target, and active accounts nobody owns now.",
		Period:      true, Application: true,
		Tables: []Table{
			{
				Name: "leavers", Title: "Leavers",
				Description: "Hours count from when Grantra recorded the leaver to the last account it disabled or membership it removed. Target: Settings → General → Compliance reports.",
				Columns: columns("person", "Person", "email", "Email", "employee_id", "Employee ID", "last_day", "Last day (HR)",
					"left_at", "Leaver recorded (UTC)", "active_accounts", "Accounts still active", "applications", "Applications with an active account",
					"closed_at", "Last account closed (UTC)", "hours", "Hours to close", "target", "Within target"),
				sql: `, sla AS (SELECT make_interval(hours => ` + settings.SQL(settings.LeaverSLAHours) + `) AS d),
				   closes AS (
				     SELECT e.details->>'identity_id' AS identity_id, max(e.occurred_at) AS at
				     FROM p CROSS JOIN audit_events e
				     WHERE e.action IN ('provisioning.disable_account', 'provisioning.remove_member')
				       AND e.details->>'status' = 'done' AND e.occurred_at >= p.since
				     GROUP BY 1)
				 SELECT i.display_name, coalesce(i.email, ''), coalesce(i.external_id, ''), ` + day("i.end_date") + `, ` + ts("l.effective_at") + `,
				        act.n::text, coalesce(act.apps, ''), ` + ts("c.at") + `, ` + hours("l.effective_at", "c.at") + `,
				        CASE WHEN i.status <> 'terminated' THEN 'came back'
				             WHEN act.n > 0 AND now() - l.effective_at > sla.d THEN 'no, still open'
				             WHEN act.n > 0 THEN 'open, in time'
				             WHEN c.at - l.effective_at > sla.d THEN 'no'
				             ELSE 'yes' END
				 FROM p CROSS JOIN sla CROSS JOIN lifecycle_events l
				 JOIN identities i ON i.id = l.identity_id
				 LEFT JOIN closes c ON c.identity_id = l.identity_id::text AND c.at >= l.effective_at
				 CROSS JOIN LATERAL (
				   SELECT count(*) AS n, string_agg(DISTINCT ap.name, ', ') AS apps
				   FROM accounts a JOIN applications ap ON ap.id = a.application_id
				   WHERE a.identity_id = l.identity_id AND a.status = 'active' AND (p.app IS NULL OR a.application_id = p.app)) act
				 WHERE l.kind = 'leaver' AND ` + within("l.effective_at") + `
				 ORDER BY l.effective_at, i.display_name`,
			},
			{
				Name: "orphans", Title: "Orphan accounts",
				Description: "Active accounts, as applications last reported them, that match nobody or belong to someone who left. Checked now.",
				Columns: columns("application", "Application", "account", "Account", "username", "Username", "reason", "Why it is an orphan",
					"privileged", "Holds high or critical risk access", "last_seen", "Last read (UTC)", "last_login", "Last sign-in (UTC)"),
				sql: `SELECT ap.name, a.native_id, coalesce(a.username, ''),
				        CASE WHEN a.identity_id IS NULL THEN 'matches nobody' ELSE 'belongs to a leaver: ' || i.display_name END,
				        ` + yesNo(catalog.AccountRiskySQL("a.id")) + `, ` + ts("a.last_seen_at") + `, ` + ts("a.last_login_at") + `
				 FROM p CROSS JOIN accounts a JOIN applications ap ON ap.id = a.application_id
				 LEFT JOIN identities i ON i.id = a.identity_id
				 WHERE a.status = 'active' AND (a.identity_id IS NULL OR i.status = 'terminated')
				   AND (p.app IS NULL OR a.application_id = p.app)
				 ORDER BY ap.name, a.native_id`,
			},
		},
		facts: []fact{
			{label: "Leavers", table: "leavers"},
			{label: "Closed in time", table: "leavers", column: "target", values: []string{"yes"}},
			{label: "Closed late or still open", table: "leavers", column: "target", values: []string{"no", "no, still open"}},
			{label: "Orphan accounts", table: "orphans"},
		},
	},
	{
		ID:          Requests,
		Title:       "Access requests and approvals",
		Description: "Every request made in the period: who asked, for whom, why, the approval policy that applied, every step and who approved it.",
		Period:      true, Application: true,
		Tables: []Table{
			{
				Name: "requests", Title: "Requests",
				Columns: columns("request", "Request", "created_at", "Asked (UTC)", "requester", "Asked by", "beneficiary", "For", "email", "Email",
					"kind", "Kind", "access", "Access", "application", "Application", "risk", "Risk", "justification", "Justification",
					"duration", "For how long", "policy", "Approval policy", "status", "Status", "closed_at", "Closed (UTC)", "reason", "Why it closed",
					"approved_by", "Approved by", "sod_risk", "Separation of duties risk"),
				sql: `SELECT r.id::text, ` + ts("r.created_at") + `, ` + person("r.requester_id") + `, ` + person("r.beneficiary_id") + `,
				        ` + email("r.beneficiary_id") + `, r.kind, ` + access("r.role_id", "r.entitlement_id") + `, ` + application("r.entitlement_id") + `,
				        ` + risk("r.entitlement_id") + `, r.justification,
				        CASE WHEN r.duration_hours IS NOT NULL THEN r.duration_hours || ' hours'
				             WHEN r.duration_days IS NOT NULL THEN r.duration_days || ' days' ELSE 'until removed' END,
				        coalesce(r.policy_name || coalesce(' (version ' || r.policy_version || ')', ''), ''),
				        r.status, ` + ts("r.closed_at") + `, coalesce(r.reason, ''),
				        coalesce((SELECT string_agg(i.display_name, ', ' ORDER BY ap.position, ap.approved_at)
				                  FROM access_request_approvals ap JOIN identities i ON i.id = ap.identity_id WHERE ap.request_id = r.id), ''),
				        coalesce((SELECT CASE s.status WHEN 'approved' THEN 'accepted' ELSE s.status END FROM access_request_steps s
				                  WHERE s.request_id = r.id AND s.kind = 'risk' ORDER BY s.position DESC LIMIT 1), '')
				 FROM p CROSS JOIN access_requests r
				 WHERE ` + within("r.created_at") + ` AND ` + inApplication("r.role_id", "r.entitlement_id") + `
				 ORDER BY r.created_at, r.id`,
			},
			{
				Name: "steps", Title: "Approval steps",
				Description: "Each step of each request, in order, with everyone asked and everyone who approved.",
				Columns: columns("request", "Request", "step", "Step", "name", "Name", "kind", "Who approves", "required", "Approvals needed",
					"asked", "Asked", "status", "Status", "approvals", "Approved by", "decided_by", "Decided by", "decided_at", "Decided (UTC)",
					"comment", "Comment", "escalated_at", "Escalated (UTC)"),
				sql: `SELECT r.id::text, s.position::text, coalesce(s.label, s.kind), s.kind, s.required::text,
				        coalesce((SELECT string_agg(i.display_name, ', ' ORDER BY i.display_name) FROM identities i WHERE i.id = ANY (s.approver_ids)), ''),
				        s.status,
				        coalesce((SELECT string_agg(i.display_name || ' at ' || ` + ts("ap.approved_at") + `, ', ' ORDER BY ap.approved_at)
				                  FROM access_request_approvals ap JOIN identities i ON i.id = ap.identity_id
				                  WHERE ap.request_id = s.request_id AND ap.position = s.position), ''),
				        CASE WHEN s.decided_by IS NULL THEN '' ELSE ` + actor("s.decided_by") + ` END, ` + ts("s.decided_at") + `,
				        coalesce(s.comment, ''), ` + ts("s.escalated_at") + `
				 FROM p CROSS JOIN access_requests r JOIN access_request_steps s ON s.request_id = r.id
				 WHERE ` + within("r.created_at") + ` AND ` + inApplication("r.role_id", "r.entitlement_id") + `
				 ORDER BY r.created_at, r.id, s.position`,
			},
		},
		facts: []fact{
			{label: "Requests", table: "requests"},
			{label: "Approved", table: "requests", column: "status", values: []string{"approved"}},
			{label: "Rejected", table: "requests", column: "status", values: []string{"rejected"}},
			{label: "Separation of duties risk accepted", table: "requests", column: "sod_risk", values: []string{"accepted"}},
		},
	},
	{
		ID:          SoD,
		Title:       "Separation of duties",
		Description: "The policies on access nobody should hold together, every violation open in the period, and every exception accepted, by whom, why and until when.",
		Period:      true,
		Tables: []Table{
			{
				Name: "policies", Title: "Policies",
				Description: "The policies as they are now.",
				Columns: columns("policy", "Policy", "severity", "Severity", "mode", "Mode", "enabled", "Enabled", "side_a", "Side A", "side_b", "Side B",
					"description", "Description"),
				sql: `SELECT sp.name, sp.severity, sp.mode, ` + yesNo("sp.enabled") + `,
				        coalesce((SELECT string_agg(` + access("s.role_id", "s.entitlement_id") + `, ', ' ORDER BY 1) FROM sod_policy_sides s
				                  WHERE s.policy_id = sp.id AND s.side = 'a'), ''),
				        coalesce((SELECT string_agg(` + access("s.role_id", "s.entitlement_id") + `, ', ' ORDER BY 1) FROM sod_policy_sides s
				                  WHERE s.policy_id = sp.id AND s.side = 'b'), ''),
				        coalesce(sp.description, '')
				 FROM p CROSS JOIN sod_policies sp ORDER BY sp.name`,
			},
			{
				Name: "violations", Title: "Violations",
				Description: "Violations open at some point of the period, with their exceptions.",
				Columns: columns("policy", "Policy", "severity", "Severity", "person", "Person", "email", "Email", "status", "Status",
					"detected_at", "Detected (UTC)", "resolved_at", "Resolved (UTC)", "exception_by", "Exception accepted by",
					"exception_reason", "Why", "exception_until", "Exception until (UTC)", "request", "Accepted on request"),
				sql: `SELECT sp.name, sp.severity, ` + person("v.identity_id") + `, ` + email("v.identity_id") + `, v.status,
				        ` + ts("v.detected_at") + `, ` + ts("v.resolved_at") + `,
				        CASE WHEN v.exception_by IS NULL THEN '' ELSE ` + actor("v.exception_by") + ` END,
				        coalesce(v.exception_reason, ''), ` + ts("v.exception_until") + `, ` + text("v.request_id") + `
				 FROM p CROSS JOIN sod_violations v JOIN sod_policies sp ON sp.id = v.policy_id
				 WHERE ` + during("v.detected_at", "v.resolved_at") + `
				 ORDER BY v.detected_at, sp.name`,
			},
		},
		facts: []fact{
			{label: "Policies", table: "policies"},
			{label: "Violations", table: "violations"},
			{label: "Open", table: "violations", column: "status", values: []string{"open"}},
			{label: "Accepted as exceptions", table: "violations", column: "status", values: []string{"excepted"}},
		},
	},
	{
		ID:          Privileged,
		Title:       "Privileged access",
		Description: "Just-in-time activations, vault check-outs and break-glass uses with their reviews, and the recorded sessions to servers, databases, clusters and desktops.",
		Period:      true, Application: true,
		Tables: []Table{
			{
				Name: "activations", Title: "Just-in-time activations",
				Columns: columns("activated_at", "Activated (UTC)", "person", "Person", "access", "Access", "application", "Application",
					"hours", "Hours", "expires_at", "Ends (UTC)", "justification", "Justification", "request", "Request", "activated_by", "Activated by"),
				sql: `SELECT ` + ts("e.occurred_at") + `, ` + person(`(e.target_id)::uuid`) + `, ` + access(`(e.details->>'role_id')::uuid`, `(e.details->>'entitlement_id')::uuid`) + `,
				        ` + application(`(e.details->>'entitlement_id')::uuid`) + `, coalesce(e.details->>'hours', ''),
				        ` + ts(`(e.details->>'expires_at')::timestamptz`) + `, coalesce(e.details->>'justification', ''),
				        coalesce(e.details->>'request_id', ''), ` + actor("e.actor") + `
				 FROM p CROSS JOIN audit_events e
				 WHERE e.action = 'access.activated' AND ` + within("e.occurred_at") + `
				   AND ` + inApplication(`(e.details->>'role_id')::uuid`, `(e.details->>'entitlement_id')::uuid`) + `
				 ORDER BY e.occurred_at, e.id`,
			},
			{
				Name: "checkouts", Title: "Vault check-outs and break-glass",
				Columns: columns("checked_out_at", "Checked out (UTC)", "account", "Vault account", "username", "Login", "person", "Person",
					"break_glass", "Break-glass", "reason", "Reason", "expires_at", "Due back (UTC)", "ended_at", "Ended (UTC)", "ended_by", "How it ended",
					"review", "Break-glass review", "reviewed_by", "Reviewed by", "reviewed_at", "Reviewed (UTC)", "review_comment", "Review comment"),
				sql: `SELECT ` + ts("c.checked_out_at") + `, va.name, va.username, ` + person("c.identity_id") + `, ` + yesNo("c.break_glass") + `,
				        c.reason, ` + ts("c.expires_at") + `, ` + ts("c.ended_at") + `, coalesce(c.ended_by, ''), coalesce(c.review_status, ''),
				        CASE WHEN c.reviewed_by IS NULL THEN '' ELSE ` + actor("c.reviewed_by") + ` END, ` + ts("c.reviewed_at") + `,
				        coalesce(c.review_comment, '')
				 FROM p CROSS JOIN vault_checkouts c JOIN vault_accounts va ON va.id = c.vault_account_id
				 WHERE ` + within("c.checked_out_at") + ` AND (p.app IS NULL OR va.entitlement_id IN (SELECT id FROM entitlements WHERE application_id = p.app))
				 ORDER BY c.checked_out_at, c.id`,
			},
			{
				Name: "sessions", Title: "Recorded sessions",
				Description: "Sessions through Grantra's SSH and remote access gateways. Their recordings are under Privileged access.",
				Columns: columns("started_at", "Started (UTC)", "kind", "Kind", "target", "Target", "login", "Login or database", "person", "Person",
					"client", "From address", "ended_at", "Ended (UTC)", "end_reason", "How it ended", "terminated_by", "Ended by an admin", "recorded", "Recorded"),
				sql: `SELECT * FROM (
				   SELECT ` + ts("s.started_at") + `, 'ssh ' || s.kind, s.target_name, s.login, ` + person("s.identity_id") + `, s.client_address,
				          ` + ts("s.ended_at") + `, coalesce(s.end_reason, ''),
				          CASE WHEN s.terminate_requested_by IS NULL THEN '' ELSE ` + actor("s.terminate_requested_by") + ` END,
				          ` + yesNo("s.recording_bytes > 0") + `
				   FROM p CROSS JOIN ssh_sessions s WHERE p.app IS NULL AND ` + within("s.started_at") + `
				   UNION ALL
				   SELECT ` + ts("g.started_at") + `, g.kind, g.target_name, concat_ws(' / ', g.account, g.database), ` + person("g.identity_id") + `,
				          g.client_address, ` + ts("g.ended_at") + `, coalesce(g.end_reason, ''),
				          CASE WHEN g.terminate_requested_by IS NULL THEN '' ELSE ` + actor("g.terminate_requested_by") + ` END,
				          ` + yesNo("g.recording_bytes > 0 OR g.events > 0") + `
				   FROM p CROSS JOIN gateway_sessions g WHERE p.app IS NULL AND ` + within("g.started_at") + `) s
				 ORDER BY 1, 3`,
			},
		},
		facts: []fact{
			{label: "Activations", table: "activations"},
			{label: "Vault check-outs", table: "checkouts"},
			{label: "Break-glass uses", table: "checkouts", column: "break_glass", values: []string{"yes"}},
			{label: "Break-glass not reviewed yet", table: "checkouts", column: "review", values: []string{"pending"}},
			{label: "Sessions", table: "sessions"},
		},
	},
	{
		ID:          Access,
		Title:       "Who has access to what",
		Description: "Everything everyone holds now: the memberships applications report, and what Grantra assigned. The population auditors sample from.",
		Application: true,
		Tables: []Table{
			{
				Name: "memberships", Title: "In the applications",
				Description: "Memberships as each application last reported them (Grantra's own platform roles included), and whether Grantra gave them.",
				Columns: columns("application", "Application", "owner", "Application owner", "account", "Account", "username", "Username",
					"account_status", "Account status", "person", "Person", "email", "Email", "person_status", "Person status",
					"entitlement", "Entitlement", "risk", "Risk", "since", "In the application since (UTC)", "expires_at", "Ends (UTC)",
					"governed", "Given through Grantra"),
				sql: `SELECT ap.name, ` + person("ap.owner_id") + `, a.native_id, coalesce(a.username, ''), a.status,
				        coalesce(i.display_name, ''), coalesce(i.email, ''), coalesce(i.status, ''), e.name, e.risk,
				        ` + ts("ae.granted_at") + `, ` + ts("ae.expires_at") + `,
				        ` + yesNo("ap.connector = '"+authz.BuiltinConnector+"' OR (a.identity_id IS NOT NULL AND "+catalog.HeldSQL("a.identity_id", "ae.entitlement_id")+")") + `
				 FROM p CROSS JOIN account_entitlements ae
				 JOIN accounts a ON a.id = ae.account_id JOIN entitlements e ON e.id = ae.entitlement_id
				 JOIN applications ap ON ap.id = a.application_id LEFT JOIN identities i ON i.id = a.identity_id
				 WHERE a.status <> 'deleted' AND (p.app IS NULL OR a.application_id = p.app)
				 ORDER BY ap.name, i.display_name NULLS FIRST, a.native_id, e.name`,
			},
			{
				Name: "assignments", Title: "Assigned in Grantra",
				Description: "What Grantra says each person should hold, and why.",
				Columns: columns("person", "Person", "email", "Email", "person_status", "Person status", "access", "Access", "type", "Type",
					"application", "Application", "risk", "Risk", "origin", "How it was given", "granted_by", "Given by", "granted_at", "Given (UTC)",
					"expires_at", "Ends (UTC)", "request", "Request"),
				sql: `SELECT i.display_name, coalesce(i.email, ''), i.status, ` + access("x.role_id", "x.entitlement_id") + `,
				        CASE WHEN x.role_id IS NULL THEN 'entitlement' ELSE 'business role' END, ` + application("x.entitlement_id") + `,
				        ` + risk("x.entitlement_id") + `, x.origin, ` + actor("x.granted_by") + `, ` + ts("x.granted_at") + `, ` + ts("x.expires_at") + `,
				        ` + text("x.request_id") + `
				 FROM p CROSS JOIN access_assignments x JOIN identities i ON i.id = x.identity_id
				 WHERE (x.expires_at IS NULL OR x.expires_at > now()) AND ` + inApplication("x.role_id", "x.entitlement_id") + `
				 ORDER BY i.display_name, i.id, 4`,
			},
		},
		facts: []fact{
			{label: "Memberships", table: "memberships"},
			{label: "High or critical risk", table: "memberships", column: "risk", values: []string{"high", "critical"}},
			{label: "Not given through Grantra", table: "memberships", column: "governed", values: []string{"no"}},
			{label: "Assignments", table: "assignments"},
		},
	},
	accessHistory,
	{
		ID:          Admin,
		Title:       "Administration changes",
		Description: "Who holds Grantra's admin, helpdesk and auditor roles, and every change to how Grantra is set up in the period, including refused attempts.",
		Period:      true,
		Tables: []Table{
			{
				Name: "platform_roles", Title: "Platform roles",
				Description: "Who holds each platform role now.",
				Columns:     columns("role", "Role", "person", "Person", "email", "Email", "status", "Person status", "granted_at", "Since (UTC)", "expires_at", "Ends (UTC)"),
				sql: `SELECT e.native_id, i.display_name, coalesce(i.email, ''), i.status, ` + ts("ae.granted_at") + `, ` + ts("ae.expires_at") + `
				 FROM p CROSS JOIN entitlements e
				 JOIN applications app ON app.id = e.application_id AND app.connector = '` + authz.BuiltinConnector + `'
				 JOIN account_entitlements ae ON ae.entitlement_id = e.id
				 JOIN accounts a ON a.id = ae.account_id AND a.status = 'active'
				 JOIN identities i ON i.id = a.identity_id
				 WHERE ae.expires_at IS NULL OR ae.expires_at > now()
				 ORDER BY e.native_id, i.display_name, i.id`,
			},
			{
				Name: "changes", Title: "Changes",
				Description: "From the audit log, which nobody can edit or delete.",
				Columns: columns("occurred_at", "When (UTC)", "who", "Who", "actor", "Actor", "action", "Action", "target_type", "Target type",
					"target", "Target", "details", "Details"),
				sql: `SELECT ` + ts("e.occurred_at") + `, ` + actor("e.actor") + `, e.actor, e.action, coalesce(e.target_type, ''),
				        CASE WHEN e.target_type = 'identity' THEN coalesce(nullif(` + person(db.IdentityIDSQL("e.target_id")) + `, ''), e.target_id)
				             ELSE coalesce(e.target_id, '') END,
				        e.details::text
				 FROM p CROSS JOIN audit_events e
				 WHERE ` + within("e.occurred_at") + ` AND ` + actionMatches("e.action", adminActions) + `
				 ORDER BY e.id`,
			},
		},
		facts: []fact{
			{label: "Admins", table: "platform_roles", column: "role", values: []string{authz.RoleAdmin}},
			{label: "Changes", table: "changes"},
			{label: "Refused attempts", table: "changes", column: "action", values: []string{"authz.denied"}},
		},
	},
	{
		ID:          Threats,
		Title:       "Identity threats and non-human identities",
		Description: "Threat alerts open in the period and how they were handled, the responses taken, and service accounts, agents and workloads that need an owner or are past their end date.",
		Period:      true,
		Tables: []Table{
			{
				Name: "alerts", Title: "Alerts",
				Columns: columns("first_seen", "First seen (UTC)", "detection", "Detection", "severity", "Severity", "about", "About",
					"summary", "Summary", "occurrences", "Times seen", "last_seen", "Last seen (UTC)", "status", "Status", "closed_by", "Closed by",
					"closed_at", "Closed (UTC)", "note", "Note", "hours", "Hours to close"),
				sql: `SELECT ` + ts("t.first_seen_at") + `, t.detection, t.severity,
				        CASE WHEN t.identity_id IS NULL THEN t.subject ELSE ` + person("t.identity_id") + ` END, t.summary, t.occurrences::text,
				        ` + ts("t.last_seen_at") + `, t.status, CASE WHEN t.closed_by IS NULL THEN '' ELSE ` + actor("t.closed_by") + ` END,
				        ` + ts("t.closed_at") + `, coalesce(t.close_note, ''), ` + hours("t.first_seen_at", "t.closed_at") + `
				 FROM p CROSS JOIN threat_alerts t
				 WHERE ` + during("t.first_seen_at", "t.closed_at") + `
				 ORDER BY t.first_seen_at, t.id`,
			},
			{
				Name: "responses", Title: "Responses",
				Columns: columns("created_at", "When (UTC)", "person", "Person", "action", "Response", "by", "By", "reason", "Reason",
					"alert", "Alert", "error", "What could not be done"),
				sql: `SELECT ` + ts("r.created_at") + `, ` + person("r.identity_id") + `, r.action, ` + actor("r.actor") + `, coalesce(r.reason, ''),
				        coalesce((SELECT summary FROM threat_alerts t WHERE t.id = r.alert_id), ''), coalesce(r.error, '')
				 FROM p CROSS JOIN threat_responses r
				 WHERE ` + within("r.created_at") + `
				 ORDER BY r.created_at, r.id`,
			},
			{
				Name: "non_human", Title: "Non-human identities",
				Description: "Service accounts, agents and workloads now, and what needs attention.",
				Columns: columns("name", "Name", "kind", "Kind", "status", "Status", "owner", "Owner", "owner_status", "Owner status",
					"purpose", "Purpose", "end_date", "End date", "attention", "Needs attention"),
				sql: `SELECT n.display_name, n.kind, n.status, coalesce(o.display_name, ''), coalesce(o.status, ''), coalesce(n.purpose, ''),
				        ` + day("n.end_date") + `,
				        CASE WHEN n.status = 'terminated' THEN ''
				             WHEN n.manager_id IS NULL THEN 'no owner'
				             WHEN o.status = 'terminated' THEN 'owner left'
				             WHEN n.end_date < current_date THEN 'past end date'
				             ELSE '' END
				 FROM p CROSS JOIN identities n LEFT JOIN identities o ON o.id = n.manager_id
				 WHERE n.kind <> 'human'
				 ORDER BY n.display_name, n.id`,
			},
		},
		facts: []fact{
			{label: "Alerts", table: "alerts"},
			{label: "Critical or high", table: "alerts", column: "severity", values: []string{"critical", "high"}},
			{label: "Still open", table: "alerts", column: "status", values: []string{"open"}},
			{label: "Non-human identities needing attention", table: "non_human", column: "attention", values: []string{"no owner", "owner left", "past end date"}},
		},
	},
}
