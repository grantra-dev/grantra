-- Gateway access beyond SSH: Kubernetes clusters and PostgreSQL and MySQL
-- databases people reach through the gateway with a short-lived token,
-- never with the credentials of the account the gateway uses there (those
-- stay in the vault). Every request or query is logged.
--
-- Who may use a target is an entitlement (in the "Databases and clusters"
-- application), so requests, approvals, reviews, separation of duties and
-- expiry govern it like any other access.

-- The tenant's CA for the gateway's TLS certificates, so kubeconfigs and
-- database clients can verify the gateway. Its private key is sealed like
-- vault secrets.
CREATE TABLE gateway_tls_cas (
    tenant_id   uuid PRIMARY KEY REFERENCES tenants (id) ON DELETE CASCADE,
    certificate text NOT NULL,
    key_id      text NOT NULL,
    wrapped_key bytea NOT NULL,
    nonce       bytea NOT NULL,
    ciphertext  bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE gateway_targets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    -- What people connect as: the database user, or the cluster's path on
    -- the gateway.
    name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    kind           text NOT NULL CHECK (kind IN ('kubernetes', 'postgres', 'mysql')),
    -- The API server's URL for a cluster, host:port for a database.
    address        text NOT NULL,
    -- The database opened when the client names none.
    database       text,
    -- How the gateway talks TLS to the target: verify its certificate,
    -- require TLS without verifying it, or none (databases only).
    tls_mode       text NOT NULL DEFAULT 'verify' CHECK (tls_mode IN ('verify', 'require', 'disable')),
    -- PEM CA certificates to verify the target with, instead of the
    -- system's.
    ca_certificate text,
    -- The account the gateway logs in with: a database login, or a
    -- Kubernetes service account token. None on a cluster means the
    -- gateway's own service account, when it runs in that cluster.
    vault_account_id uuid REFERENCES vault_accounts (id) ON DELETE SET NULL,
    -- The groups a person acts as on a cluster (impersonated).
    kube_groups    text[] NOT NULL DEFAULT '{}',
    notes          text,
    entitlement_id uuid NOT NULL REFERENCES entitlements (id) ON DELETE RESTRICT,
    max_token_hours integer NOT NULL DEFAULT 8 CHECK (max_token_hours BETWEEN 1 AND 24),
    created_by     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (entitlement_id),
    CHECK (kind <> 'kubernetes' OR tls_mode <> 'disable')
);

-- Tokens people connect with. Only their SHA-256 is kept.
CREATE TABLE gateway_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    target_id    uuid NOT NULL REFERENCES gateway_targets (id) ON DELETE CASCADE,
    identity_id  uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    valid_before timestamptz NOT NULL,
    issued_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    revoked_by   text
);

CREATE INDEX gateway_tokens_identity ON gateway_tokens (tenant_id, identity_id, issued_at DESC);

-- One database connection, or a run of requests to a cluster with one
-- token, through one gateway.
CREATE TABLE gateway_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    target_id      uuid REFERENCES gateway_targets (id) ON DELETE SET NULL,
    target_name    text NOT NULL,
    kind           text NOT NULL,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    token_id       uuid REFERENCES gateway_tokens (id) ON DELETE SET NULL,
    client_address text NOT NULL,
    gateway        text NOT NULL,
    -- The database opened, and the account the gateway logged in as.
    database       text,
    account        text,
    started_at     timestamptz NOT NULL DEFAULT now(),
    -- The gateway touches this while the session runs; a session it
    -- stopped touching was lost with its gateway.
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    ended_at       timestamptz,
    -- closed by the person (or idle, on a cluster), access_ended when the
    -- access or the token ran out, terminated by an admin, lost with its
    -- gateway, failed, or recording_full when its log reached the limit.
    end_reason     text CHECK (end_reason IN ('closed', 'access_ended', 'terminated', 'lost', 'failed', 'recording_full')),
    error          text,
    terminate_requested_by text,
    events         integer NOT NULL DEFAULT 0,
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);

CREATE INDEX gateway_sessions_started ON gateway_sessions (tenant_id, started_at DESC);
CREATE INDEX gateway_sessions_open ON gateway_sessions (tenant_id, last_seen_at) WHERE ended_at IS NULL;

-- The queries run in a database session, the requests made to a cluster.
CREATE TABLE gateway_events (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES gateway_sessions (id) ON DELETE CASCADE,
    at         timestamptz NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('query', 'request')),
    text       text NOT NULL,
    -- The HTTP status of a cluster request.
    status     integer
);

CREATE INDEX gateway_events_at ON gateway_events (tenant_id, at DESC);
CREATE INDEX gateway_events_session ON gateway_events (session_id, id);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['gateway_tls_cas', 'gateway_targets', 'gateway_tokens', 'gateway_sessions', 'gateway_events'] LOOP
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
