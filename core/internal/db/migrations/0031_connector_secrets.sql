-- Connector secrets typed in the console (API tokens, client secrets,
-- service account keys). Each is sealed like a vault secret: AES-256-GCM
-- under its own data key, which the vault key wraps. Connector configs
-- refer to one as "secret:<id>"; the value is never shown again.
CREATE TABLE connector_secrets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    label       text NOT NULL,
    key_id      text NOT NULL,
    wrapped_key bytea NOT NULL,
    nonce       bytea NOT NULL,
    ciphertext  bytea NOT NULL,
    created_by  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE connector_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON connector_secrets
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
