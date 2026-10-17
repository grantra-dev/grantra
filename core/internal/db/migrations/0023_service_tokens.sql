-- Service tokens let a company's scripts and scheduled jobs feed Grantra
-- without a person signing in: a nightly job that posts the HR export, or
-- the account list of an application with no API. Each token may do only
-- what its scopes say, and ends on its expiry date or when revoked.
--
-- Only a SHA-256 of each token is kept; prefix is its first characters, for
-- people to tell tokens apart.
CREATE TABLE service_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL,
    hash         bytea NOT NULL UNIQUE,
    -- Each scope is an action, alone or pinned to one source or
    -- application: "identity_source.sync" or "identity_source.sync:<id>".
    scopes       text[] NOT NULL CHECK (cardinality(scopes) > 0),
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX service_tokens_tenant ON service_tokens (tenant_id, created_at DESC);

ALTER TABLE service_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON service_tokens
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
