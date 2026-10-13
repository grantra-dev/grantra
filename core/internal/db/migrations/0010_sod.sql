-- Separation of duties (SoD): a policy names two sets of access that one
-- person must not hold together, such as creating payments and approving
-- them. Grantra finds who holds both (violations), can refuse access
-- requests that would create one, and records approved exceptions.

CREATE TABLE sod_policies (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text,
    severity    text NOT NULL DEFAULT 'high' CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    -- 'detect' only reports violations; 'prevent' also refuses access
    -- requests that would create one.
    mode        text NOT NULL DEFAULT 'detect' CHECK (mode IN ('detect', 'prevent')),
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- The two sides of a policy: business roles and entitlements.
CREATE TABLE sod_policy_sides (
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    policy_id      uuid NOT NULL REFERENCES sod_policies (id) ON DELETE CASCADE,
    side           text NOT NULL CHECK (side IN ('a', 'b')),
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL))
);

CREATE UNIQUE INDEX sod_policy_sides_unique
    ON sod_policy_sides (policy_id, side, coalesce(role_id, entitlement_id));

-- A person found holding both sides of a policy. It stays open until they
-- no longer do (resolved) or an admin accepts the risk (an exception).
CREATE TABLE sod_violations (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    policy_id         uuid NOT NULL REFERENCES sod_policies (id) ON DELETE CASCADE,
    identity_id       uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'excepted', 'resolved')),
    detected_at       timestamptz NOT NULL DEFAULT now(),
    resolved_at       timestamptz,
    exception_by      text,
    exception_reason  text,
    exception_until   timestamptz,
    CHECK ((status = 'excepted') = (exception_by IS NOT NULL))
);

-- One live violation per person and policy.
CREATE UNIQUE INDEX sod_violations_live
    ON sod_violations (policy_id, identity_id) WHERE status <> 'resolved';
CREATE INDEX ON sod_violations (tenant_id, status);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['sod_policies', 'sod_policy_sides', 'sod_violations'] LOOP
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
