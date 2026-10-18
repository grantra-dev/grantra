-- Parts of Grantra a tenant turned off in the console, such as privileged
-- access for a company that only governs access. A module with no row is
-- on. Turning a module off hides its pages, search results and tasks; it
-- does not change anyone's access.
CREATE TABLE tenant_modules (
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    module     text NOT NULL,
    enabled    boolean NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, module)
);

ALTER TABLE tenant_modules ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_modules FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_modules
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Finding people by any part of their name or email, for the console's
-- search box and the People page. Like 0022, the index needs pg_trgm, and
-- search works without it, only slower.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
    CREATE INDEX identities_name_text ON identities USING gin (lower(display_name) gin_trgm_ops);
    CREATE INDEX identities_email_text ON identities USING gin (lower(email) gin_trgm_ops);
EXCEPTION WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
    RAISE NOTICE 'pg_trgm is not available (%), so people searches use no index', SQLERRM;
END;
$$;
