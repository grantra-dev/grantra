-- How a tenant has Grantra set up: time limits, reminders and thresholds an
-- admin changes in the console under Settings → General. The settings
-- themselves, with their defaults and limits, are defined in code (package
-- settings); a row holds only a value an admin changed, so a setting with no
-- row has its default and new versions bring new defaults along.
CREATE TABLE tenant_settings (
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    key        text NOT NULL,
    value      integer NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);

ALTER TABLE tenant_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_settings
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- A change that makes Grantra less strict, such as a longer break-glass
-- check-out, waits here for a second admin. One change waits per setting.
CREATE TABLE tenant_setting_changes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    key         text NOT NULL,
    old_value   integer NOT NULL,
    value       integer NOT NULL,
    reason      text,
    proposed_by text NOT NULL,
    proposed_at timestamptz NOT NULL DEFAULT now(),
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'withdrawn')),
    decided_by  text,
    decided_at  timestamptz,
    comment     text
);
CREATE UNIQUE INDEX tenant_setting_changes_pending ON tenant_setting_changes (tenant_id, key) WHERE status = 'pending';

ALTER TABLE tenant_setting_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_setting_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_setting_changes
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
