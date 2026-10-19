-- Identity threat detection and response.
--
-- Sign-ins come from Grantra Login (read from Keycloak's event store) and
-- from any other identity provider a script feeds through the API. Grantra
-- looks at each one once (evaluated), and raises alerts about people (or,
-- for a password spray, about an address) that security can respond to.

CREATE TABLE signin_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    -- "grantra-login", or the name a script gives its provider ("okta").
    source      text NOT NULL,
    -- The provider's own id for the event, so feeding the same events twice
    -- stores them once.
    external_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    identity_id uuid REFERENCES identities (id) ON DELETE SET NULL,
    -- The name the person typed, also when it matches nobody.
    username    text,
    -- failure is a wrong password or an unknown user; mfa_failure is a
    -- second factor refused or wrong, which means the password was right.
    outcome     text NOT NULL CHECK (outcome IN ('success', 'failure', 'mfa_failure')),
    ip          inet,
    country     text CHECK (country ~ '^[A-Z]{2}$'),
    latitude    double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude   double precision CHECK (longitude BETWEEN -180 AND 180),
    application text,
    user_agent  text,
    evaluated   boolean NOT NULL DEFAULT false,
    UNIQUE (tenant_id, source, external_id)
);

CREATE INDEX signin_events_identity ON signin_events (tenant_id, identity_id, occurred_at DESC);
CREATE INDEX signin_events_time ON signin_events (tenant_id, occurred_at DESC);
CREATE INDEX signin_events_ip ON signin_events (tenant_id, ip, occurred_at) WHERE outcome <> 'success';
CREATE INDEX signin_events_pending ON signin_events (tenant_id, occurred_at, id) WHERE NOT evaluated;

-- Where reading a source got to, such as the time of the last Grantra Login
-- event read, or the last account change looked at for privilege changes.
CREATE TABLE itdr_cursors (
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL,
    position   text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, name)
);

-- How each detection is set up, when an admin changed it from its default.
CREATE TABLE threat_detections (
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    detection     text NOT NULL,
    enabled       boolean NOT NULL,
    severity      text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    -- What Grantra does on its own when the detection fires.
    auto_response text NOT NULL DEFAULT 'none',
    params        jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_by    text NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, detection)
);

CREATE TABLE threat_alerts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    detection     text NOT NULL,
    severity      text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'dismissed')),
    identity_id   uuid REFERENCES identities (id) ON DELETE CASCADE,
    -- Who or what the alert is about: an identity id, or "ip:<address>".
    -- While an alert is open, the same detection about the same subject
    -- adds to it instead of raising another.
    subject       text NOT NULL,
    summary       text NOT NULL,
    evidence      jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurrences   int NOT NULL DEFAULT 1,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    closed_by     text,
    closed_at     timestamptz,
    close_note    text,
    CHECK ((status = 'open') = (closed_at IS NULL))
);

CREATE UNIQUE INDEX threat_alerts_open ON threat_alerts (tenant_id, detection, subject) WHERE status = 'open';
CREATE INDEX threat_alerts_list ON threat_alerts (tenant_id, status, last_seen_at DESC);
CREATE INDEX threat_alerts_identity ON threat_alerts (tenant_id, identity_id, last_seen_at DESC);

-- What was done about a threat to someone: their sessions ended, a second
-- factor required, their identity suspended or reinstated. details keeps
-- what reinstating needs, such as the accounts a suspension disabled.
CREATE TABLE threat_responses (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    alert_id    uuid REFERENCES threat_alerts (id) ON DELETE SET NULL,
    action      text NOT NULL CHECK (action IN ('revoke_sessions', 'require_mfa', 'suspend', 'reinstate')),
    actor       text NOT NULL,
    reason      text,
    details     jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Set when part of the response could not be done, such as Grantra
    -- Login being down; the rest was done.
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX threat_responses_identity ON threat_responses (tenant_id, identity_id, created_at DESC);
CREATE INDEX threat_responses_alert ON threat_responses (alert_id) WHERE alert_id IS NOT NULL;

-- People Grantra Login asks for a second factor for a while because of a
-- threat, whatever access they hold.
CREATE TABLE mfa_overrides (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    until       timestamptz NOT NULL,
    created_by  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, identity_id)
);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['signin_events', 'itdr_cursors', 'threat_detections', 'threat_alerts', 'threat_responses', 'mfa_overrides']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid)$p$, t);
    END LOOP;
END;
$$;

-- The access scan reads, every minute, what accounts gained since its last
-- look.
CREATE INDEX account_entitlements_granted ON account_entitlements (tenant_id, granted_at);
