-- Connectors, first half: reading the accounts and permissions that exist in
-- each application (aggregation) and matching accounts to identities
-- (correlation). Writing to applications (provisioning) comes next.

-- How an account got linked to its identity: 'rule:<name>' when a
-- correlation rule matched, 'manual' when someone linked it by hand. Manual
-- links survive every aggregation.
ALTER TABLE accounts ADD COLUMN correlated_by text;

-- Entitlements and accounts that an aggregation no longer finds keep their
-- history; missing_since marks them gone from the application.
ALTER TABLE entitlements ADD COLUMN missing_since timestamptz;

-- One run of a connector against an application.
CREATE TABLE connector_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('aggregation')),
    actor          text NOT NULL,
    status         text NOT NULL CHECK (status IN ('succeeded', 'failed')),
    counts         jsonb NOT NULL DEFAULT '{}'::jsonb,
    error          text,
    started_at     timestamptz NOT NULL,
    finished_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ON connector_runs (tenant_id, application_id, started_at DESC);
CREATE INDEX ON accounts (tenant_id, application_id) WHERE identity_id IS NULL;

ALTER TABLE connector_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON connector_runs
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
