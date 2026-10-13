-- Connectors, second half: provisioning. Grantra writes to applications so
-- that accounts and their entitlements match the assignments.

-- How Grantra provisions an application. mode is 'off' (the default: Grantra
-- only reads) or 'on'. remove_unassigned lets Grantra remove memberships it
-- did not grant itself; off by default, so turning provisioning on never
-- strips access people got outside Grantra. max_change_percent refuses a run
-- that would remove or disable more than that share of accounts.
ALTER TABLE applications
    ADD COLUMN provisioning jsonb NOT NULL DEFAULT '{"mode": "off"}'::jsonb;

-- Set when Grantra itself put the membership there. Aggregations keep it on
-- memberships that are still present.
ALTER TABLE account_entitlements ADD COLUMN provisioned_at timestamptz;

ALTER TABLE connector_runs DROP CONSTRAINT connector_runs_kind_check;
ALTER TABLE connector_runs ADD CONSTRAINT connector_runs_kind_check
    CHECK (kind IN ('aggregation', 'provisioning'));
ALTER TABLE connector_runs DROP CONSTRAINT connector_runs_status_check;
ALTER TABLE connector_runs ADD CONSTRAINT connector_runs_status_check
    CHECK (status IN ('planned', 'succeeded', 'partial', 'failed', 'refused'));

-- The actions of a provisioning run, with their outcome.
ALTER TABLE connector_runs ADD COLUMN actions jsonb NOT NULL DEFAULT '[]'::jsonb;
