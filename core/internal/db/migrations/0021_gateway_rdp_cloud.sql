-- Remote desktops (RDP) and cloud consoles (AWS, Azure, Google Cloud)
-- behind the gateway, beside databases and clusters.
--
-- A desktop session runs in the browser: Grantra relays it to guacd
-- (Apache Guacamole's proxy), which logs in to the desktop with the vault
-- account, and records the screen. A cloud console session signs the person
-- in to the cloud with time-bound access Grantra grants there: temporary
-- role credentials on AWS, a role assignment on Azure, an expiring IAM
-- binding on Google Cloud.

ALTER TABLE gateway_targets DROP CONSTRAINT gateway_targets_kind_check;
ALTER TABLE gateway_targets ADD CONSTRAINT gateway_targets_kind_check
    CHECK (kind IN ('kubernetes', 'postgres', 'mysql', 'rdp', 'aws', 'azure', 'gcp'));
ALTER TABLE gateway_targets DROP CONSTRAINT gateway_targets_check;
ALTER TABLE gateway_targets ADD CONSTRAINT gateway_targets_tls_check
    CHECK (kind NOT IN ('kubernetes', 'rdp') OR tls_mode <> 'disable');

-- On a desktop, which way the clipboard works: both ways, only pasting
-- into the desktop, or not at all.
ALTER TABLE gateway_targets ADD COLUMN clipboard text CHECK (clipboard IN ('both', 'paste', 'none'));
-- On a cloud console, the role people get: an IAM role's ARN is the
-- address on AWS; here an Azure role (name or id) or a Google Cloud role
-- (roles/...).
ALTER TABLE gateway_targets ADD COLUMN cloud_role text;
-- The Azure tenant (directory) the gateway signs in to.
ALTER TABLE gateway_targets ADD COLUMN cloud_directory text;

-- What Grantra granted in the cloud for a console session (an Azure role
-- assignment, the title of a Google Cloud IAM binding), to take back when
-- the session ends.
ALTER TABLE gateway_sessions ADD COLUMN cloud_grant text;
ALTER TABLE gateway_sessions ADD COLUMN recording_bytes bigint NOT NULL DEFAULT 0;

-- A desktop session's screen recording: what guacd sent the browser, in
-- the Guacamole protocol, in the order it was sent.
CREATE TABLE gateway_recordings (
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES gateway_sessions (id) ON DELETE CASCADE,
    seq        integer NOT NULL,
    data       bytea NOT NULL,
    PRIMARY KEY (session_id, seq)
);

ALTER TABLE gateway_recordings ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway_recordings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON gateway_recordings
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- The application now holds more than databases and clusters. Row-level
-- security applies to the migration role too, so tenant by tenant.
DO $$
DECLARE
    t uuid;
BEGIN
    FOR t IN SELECT id FROM tenants LOOP
        PERFORM set_config('app.tenant_id', t::text, true);
        UPDATE applications SET name = 'Remote access'
        WHERE name = 'Databases and clusters' AND connector = 'manual'
          AND NOT EXISTS (SELECT 1 FROM applications b WHERE b.name = 'Remote access');
    END LOOP;
    PERFORM set_config('app.tenant_id', '', true);
END;
$$;
