-- SSH access: a certificate authority per tenant, the servers (targets)
-- people log in to, the short-lived certificates Grantra signs for them, and
-- the sessions the gateway records.
--
-- Who may log in to a target is an entitlement (in the "SSH servers"
-- application), so requests, approvals, reviews, separation of duties and
-- expiry govern it like any other access.

-- The tenant's SSH user CA. Servers trust its public key
-- (TrustedUserCAKeys); its private key is sealed like a vault secret.
CREATE TABLE ssh_cas (
    tenant_id   uuid PRIMARY KEY REFERENCES tenants (id) ON DELETE CASCADE,
    public_key  text NOT NULL,
    key_id      text NOT NULL,
    wrapped_key bytea NOT NULL,
    nonce       bytea NOT NULL,
    ciphertext  bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ssh_targets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    -- The name people type as the user on the gateway: ssh prod-db@gateway.
    name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    -- host:port of the server, as the gateway reaches it.
    address        text NOT NULL,
    -- The account on the server the certificate logs in as.
    login          text NOT NULL,
    notes          text,
    entitlement_id uuid NOT NULL REFERENCES entitlements (id) ON DELETE RESTRICT,
    max_certificate_hours integer NOT NULL DEFAULT 8 CHECK (max_certificate_hours BETWEEN 1 AND 24),
    -- Through the gateway only (recorded), or straight to the server.
    via_gateway    boolean NOT NULL DEFAULT true,
    -- The server's host key, learned on the gateway's first connection
    -- and required to match from then on.
    host_key       text,
    created_by     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (entitlement_id)
);

CREATE TABLE ssh_certificates (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    serial         bigint GENERATED ALWAYS AS IDENTITY,
    target_id      uuid NOT NULL REFERENCES ssh_targets (id) ON DELETE CASCADE,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    principal      text NOT NULL,
    key_fingerprint text NOT NULL,
    valid_after    timestamptz NOT NULL,
    valid_before   timestamptz NOT NULL,
    issued_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ssh_certificates_identity ON ssh_certificates (tenant_id, identity_id, issued_at DESC);

-- One shell or command run through the gateway.
CREATE TABLE ssh_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    target_id      uuid REFERENCES ssh_targets (id) ON DELETE SET NULL,
    target_name    text NOT NULL,
    login          text NOT NULL,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    certificate_id uuid REFERENCES ssh_certificates (id) ON DELETE SET NULL,
    client_address text NOT NULL,
    gateway        text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('shell', 'exec')),
    command        text,
    width          integer NOT NULL DEFAULT 80,
    height         integer NOT NULL DEFAULT 24,
    started_at     timestamptz NOT NULL DEFAULT now(),
    -- The gateway touches this while the session runs; a session it
    -- stopped touching was lost with its gateway.
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    ended_at       timestamptz,
    -- closed by either side, access_ended when access went away,
    -- terminated by an admin, lost with its gateway, or failed.
    end_reason     text CHECK (end_reason IN ('closed', 'access_ended', 'terminated', 'lost', 'failed')),
    error          text,
    terminate_requested_by text,
    exit_status    integer,
    recording_bytes bigint NOT NULL DEFAULT 0,
    recording_truncated boolean NOT NULL DEFAULT false,
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);

CREATE INDEX ssh_sessions_started ON ssh_sessions (tenant_id, started_at DESC);
CREATE INDEX ssh_sessions_open ON ssh_sessions (tenant_id, last_seen_at) WHERE ended_at IS NULL;

-- The recording, in asciicast v2 lines, in the order the gateway wrote it.
CREATE TABLE ssh_session_chunks (
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES ssh_sessions (id) ON DELETE CASCADE,
    seq        integer NOT NULL,
    data       bytea NOT NULL,
    PRIMARY KEY (session_id, seq)
);

-- Commands typed in a session (best effort: the recording is the record).
CREATE TABLE ssh_commands (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES ssh_sessions (id) ON DELETE CASCADE,
    at         timestamptz NOT NULL,
    -- Seconds into the recording, to jump to it when playing back.
    offset_seconds double precision NOT NULL,
    command    text NOT NULL
);

CREATE INDEX ssh_commands_at ON ssh_commands (tenant_id, at DESC);
CREATE INDEX ssh_commands_session ON ssh_commands (session_id, id);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['ssh_cas', 'ssh_targets', 'ssh_certificates', 'ssh_sessions', 'ssh_session_chunks', 'ssh_commands'] LOOP
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
