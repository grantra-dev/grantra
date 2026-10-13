-- Core identity model: tenants, identities, accounts, applications,
-- entitlements, roles and an append-only audit log.
--
-- Every tenant-owned table carries tenant_id and is protected by row-level
-- security. The application sets the tenant per transaction with
--   SET LOCAL app.tenant_id = '<uuid>';
-- and FORCE makes the policy apply to the table owner as well.

CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A person or non-human principal that Grantra governs.
CREATE TABLE identities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    external_id text,
    kind        text NOT NULL DEFAULT 'human' CHECK (kind IN ('human', 'service', 'agent')),
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'suspended', 'terminated')),
    display_name text NOT NULL,
    email       text,
    manager_id  uuid REFERENCES identities (id) ON DELETE SET NULL,
    attributes  jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, external_id)
);

-- A system Grantra connects to (Entra ID, LDAP, GitHub, AWS...).
CREATE TABLE applications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL,
    connector  text NOT NULL,
    owner_id   uuid REFERENCES identities (id) ON DELETE SET NULL,
    config     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- A login that exists in an application. identity_id is NULL for orphans.
CREATE TABLE accounts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    identity_id    uuid REFERENCES identities (id) ON DELETE SET NULL,
    native_id      text NOT NULL,
    username       text,
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_seen_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, native_id)
);

-- A permission inside an application (a group, an AWS policy, a repo role...).
CREATE TABLE entitlements (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    native_id      text NOT NULL,
    name           text NOT NULL,
    description    text,
    risk           text NOT NULL DEFAULT 'low' CHECK (risk IN ('low', 'medium', 'high', 'critical')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, native_id)
);

CREATE TABLE account_entitlements (
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    account_id     uuid NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    entitlement_id uuid NOT NULL REFERENCES entitlements (id) ON DELETE CASCADE,
    granted_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, entitlement_id)
);

-- A business role that bundles entitlements across applications.
CREATE TABLE roles (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text,
    owner_id    uuid REFERENCES identities (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE role_entitlements (
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    role_id        uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid NOT NULL REFERENCES entitlements (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, entitlement_id)
);

CREATE TABLE identity_roles (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    role_id     uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    granted_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz,
    PRIMARY KEY (identity_id, role_id)
);

-- Append-only record of everything that happens in the platform.
CREATE TABLE audit_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor       text NOT NULL,
    action      text NOT NULL,
    target_type text,
    target_id   text,
    details     jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX ON identities (tenant_id, status);
CREATE INDEX ON accounts (tenant_id, identity_id);
CREATE INDEX ON audit_events (tenant_id, occurred_at DESC);

CREATE FUNCTION audit_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;

CREATE TRIGGER audit_events_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();

-- Row-level security: a row is visible only when app.tenant_id matches.
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'identities', 'applications', 'accounts', 'entitlements',
        'account_entitlements', 'roles', 'role_entitlements',
        'identity_roles', 'audit_events'
    ] LOOP
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
