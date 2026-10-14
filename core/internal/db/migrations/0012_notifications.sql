-- Email notifications go through an outbox: the flow that causes one writes
-- a row in its own transaction, and a background sender delivers it. A send
-- that fails is retried and never undoes or blocks the flow.
CREATE TABLE notification_outbox (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id     uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    kind            text NOT NULL,
    -- What the template needs, captured when the event happened.
    data            jsonb NOT NULL DEFAULT '{}'::jsonb,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);

CREATE INDEX notification_outbox_due ON notification_outbox (tenant_id, next_attempt_at) WHERE status = 'pending';

-- People turn off the kinds of email they do not want. Everything is on by
-- default.
CREATE TABLE notification_opt_outs (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    kind        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (identity_id, kind)
);

-- Approvers get one reminder before a step they have not decided expires.
ALTER TABLE access_request_steps ADD COLUMN reminded_at timestamptz;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['notification_outbox', 'notification_opt_outs'] LOOP
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
