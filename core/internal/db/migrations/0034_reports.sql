-- Compliance frameworks a tenant reports against (Governance → Reports). The
-- built-in ones (SOX, ISO 27001, SOC 2, NIS2) are defined in code (package
-- reports); a row for one of them holds only what an admin changed, so a
-- framework with no row has its defaults and new versions bring new
-- defaults along. Frameworks the tenant adds itself always have a row.
CREATE TABLE report_frameworks (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    id          text NOT NULL,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description text CHECK (length(description) <= 1000),
    enabled     boolean NOT NULL DEFAULT true,
    -- [{"id": "CC6.2", "title": "...", "reports": ["requests", "leavers"]}]
    controls    jsonb NOT NULL DEFAULT '[]'::jsonb,
    updated_by  text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE report_frameworks ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_frameworks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON report_frameworks
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Every downloaded report or evidence pack is in the audit log with the
-- SHA-256 of what was downloaded, so anyone can later check a file came
-- from Grantra unchanged (Reports → Verify a pack).
CREATE INDEX audit_events_reports ON audit_events (tenant_id, (details->>'sha256'))
    WHERE action = 'report.generated';
