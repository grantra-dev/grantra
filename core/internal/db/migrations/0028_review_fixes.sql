-- Fixes from the review before v0.4.

-- Threat detection looks at each grant of access in an application once.
-- A grant waits here until a scan has looked at it, so one written by a
-- long connector run, which commits after scans that started later, is
-- still looked at. Grants already stored count as looked at.
ALTER TABLE account_entitlements ADD COLUMN threat_checked boolean NOT NULL DEFAULT true;
ALTER TABLE account_entitlements ALTER COLUMN threat_checked SET DEFAULT false;
CREATE INDEX account_entitlements_unchecked ON account_entitlements (tenant_id, granted_at) WHERE NOT threat_checked;
-- The scan read grants by time with this one; nothing else does.
DROP INDEX account_entitlements_granted;

-- Who holds a role or an entitlement: the vault, servers and gateways ask
-- it on every check-out and connection, and attack paths, insights and
-- reviews read it for whole lists. Lookups by person already have indexes.
CREATE INDEX access_assignments_entitlement ON access_assignments (tenant_id, entitlement_id) WHERE entitlement_id IS NOT NULL;
CREATE INDEX access_assignments_role ON access_assignments (tenant_id, role_id) WHERE role_id IS NOT NULL;
CREATE INDEX access_eligibilities_entitlement ON access_eligibilities (tenant_id, entitlement_id) WHERE entitlement_id IS NOT NULL;
CREATE INDEX access_eligibilities_role ON access_eligibilities (tenant_id, role_id) WHERE role_id IS NOT NULL;

-- Sign-ins from other providers name people by the username of any of
-- their accounts, in any application.
CREATE INDEX accounts_owned_username ON accounts (tenant_id, lower(username))
    WHERE status <> 'deleted' AND identity_id IS NOT NULL;
