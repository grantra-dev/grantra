-- Console login: people sign in through an OpenID Connect provider (Grantra
-- Login, the bundled Keycloak, or the company's own IdP) and get a
-- server-side session. Grantra never stores passwords.

-- Links an IdP login (issuer + subject) to the identity it belongs to. The
-- first login matches by verified email; later logins go by subject, which
-- unlike the email never changes.
CREATE TABLE identity_logins (
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    issuer        text NOT NULL,
    subject       text NOT NULL,
    identity_id   uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, issuer, subject)
);

-- A login in progress, between the redirect to the IdP and the callback.
-- Only a hash of the state is stored; the browser holds the state itself.
CREATE TABLE oidc_login_flows (
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    state_hash    bytea PRIMARY KEY,
    nonce         text NOT NULL,
    code_verifier text NOT NULL,
    return_to     text NOT NULL,
    expires_at    timestamptz NOT NULL
);

-- A signed-in browser. The cookie carries a random token; only its hash is
-- stored, so a database leak does not hand out live sessions.
CREATE TABLE sessions (
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    token_hash   bytea PRIMARY KEY,
    identity_id  uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    id_token     text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX ON identity_logins (tenant_id, identity_id);
CREATE INDEX ON oidc_login_flows (tenant_id, expires_at);
CREATE INDEX ON sessions (tenant_id, identity_id);
CREATE INDEX ON sessions (tenant_id, expires_at);
CREATE INDEX ON identities (tenant_id, lower(email));

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['identity_logins', 'oidc_login_flows', 'sessions'] LOOP
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
