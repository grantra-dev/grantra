-- Authorization for a company's own apps: each application has Cedar
-- policies, and API keys its backend uses to ask Grantra whether someone may
-- do something.
CREATE TABLE app_authz_policies (
    application_id uuid PRIMARY KEY REFERENCES applications (id) ON DELETE CASCADE,
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    policies       text NOT NULL,
    updated_by     text NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Only a SHA-256 of each key is kept; prefix is its first characters, for
-- people to tell keys apart.
CREATE TABLE app_api_keys (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    name           text NOT NULL,
    prefix         text NOT NULL,
    hash           bytea NOT NULL UNIQUE,
    created_by     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz,
    revoked_at     timestamptz
);

CREATE INDEX app_api_keys_application ON app_api_keys (application_id);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['app_authz_policies', 'app_api_keys'] LOOP
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
