-- Assigned access and birthright rules.
--
-- An assignment says an identity *should* have a business role or an
-- entitlement, and why: a birthright rule, a manual grant or an approved
-- request. It is the desired state. Accounts and account_entitlements remain
-- the actual state found in (or pushed to) each application; connectors
-- reconcile one with the other.
--
-- identity_roles from 0001 never got used and carried no reason, so
-- access_assignments replaces it.

DROP TABLE identity_roles;

-- A birthright rule: everyone whose attributes match gets the rule's access,
-- and loses it when they stop matching. conditions maps an attribute to the
-- values it may take, e.g. {"department": ["Sales", "Marketing"]}: every
-- attribute must match one of its values. {} matches everyone.
CREATE TABLE access_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text,
    conditions  jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- What a rule gives: business roles and entitlements.
CREATE TABLE access_rule_grants (
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    rule_id        uuid NOT NULL REFERENCES access_rules (id) ON DELETE CASCADE,
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL))
);

CREATE UNIQUE INDEX access_rule_grants_unique
    ON access_rule_grants (rule_id, coalesce(role_id, entitlement_id));

CREATE TABLE access_assignments (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    origin         text NOT NULL CHECK (origin IN ('birthright', 'manual', 'request')),
    rule_id        uuid REFERENCES access_rules (id) ON DELETE CASCADE,
    granted_by     text NOT NULL,
    granted_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL)),
    CHECK ((origin = 'birthright') = (rule_id IS NOT NULL))
);

-- The same access can be held for several reasons (two rules, a rule and a
-- request), each its own row, but never twice for the same reason.
CREATE UNIQUE INDEX access_assignments_unique
    ON access_assignments (identity_id, coalesce(role_id, entitlement_id), origin,
                           coalesce(rule_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX ON access_assignments (tenant_id, identity_id);
CREATE INDEX ON access_assignments (tenant_id, rule_id);
CREATE INDEX ON role_entitlements (tenant_id, entitlement_id);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['access_rules', 'access_rule_grants', 'access_assignments'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I
                USING (tenant_id = current_setting(''app.tenant_id'', true)::uuid)
                WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true)::uuid)',
            t);
    END LOOP;
END;
$$;
