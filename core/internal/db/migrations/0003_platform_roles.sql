-- Platform roles: who administers, supports and audits Grantra itself.
--
-- Grantra governs itself like any other application (dogfooding). Each tenant
-- has one built-in application with connector 'grantra' whose entitlements are
-- the platform roles (grantra-admin, grantra-helpdesk, grantra-auditor). Holding
-- a role means having an active account in that application with the role's
-- entitlement, so platform roles can later be requested, approved, reviewed
-- and expired like any other access.

-- Time-bound access, for platform roles and for any other entitlement.
ALTER TABLE account_entitlements ADD COLUMN expires_at timestamptz;

CREATE UNIQUE INDEX applications_one_builtin ON applications (tenant_id) WHERE connector = 'grantra';
CREATE INDEX ON account_entitlements (tenant_id, entitlement_id);
CREATE INDEX ON identities (tenant_id, manager_id);
CREATE INDEX ON applications (tenant_id, owner_id);
