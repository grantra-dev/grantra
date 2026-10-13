-- Access requests: someone asks for a business role or an entitlement, for
-- themselves or for one of their reports, and approvers decide in order: the
-- person's manager, then the owner of the application or role. An approved
-- request becomes an assignment with origin 'request', which provisioning
-- then carries to the application.

CREATE TABLE access_requests (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    requester_id   uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    beneficiary_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    justification  text NOT NULL,
    -- How long the access lasts once granted; NULL means until removed.
    duration_days  integer CHECK (duration_days BETWEEN 1 AND 365),
    status         text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    -- Why a request was cancelled or expired.
    reason         text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    closed_at      timestamptz,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL)),
    CHECK ((status = 'pending') = (closed_at IS NULL))
);

-- One open request per person and access.
CREATE UNIQUE INDEX access_requests_one_pending
    ON access_requests (beneficiary_id, coalesce(role_id, entitlement_id)) WHERE status = 'pending';
CREATE INDEX ON access_requests (tenant_id, requester_id, created_at DESC);
CREATE INDEX ON access_requests (tenant_id, beneficiary_id, created_at DESC);

-- The approvals a request needs, decided in position order. Only one step is
-- pending at a time; the ones after it are waiting. approver_id NULL means
-- any admin decides, used when there is nobody else to ask.
CREATE TABLE access_request_steps (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    request_id  uuid NOT NULL REFERENCES access_requests (id) ON DELETE CASCADE,
    position    integer NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('manager', 'owner', 'admin')),
    approver_id uuid REFERENCES identities (id) ON DELETE SET NULL,
    status      text NOT NULL CHECK (status IN ('waiting', 'pending', 'approved', 'rejected', 'skipped')),
    due_at      timestamptz,
    decided_by  text,
    decided_at  timestamptz,
    comment     text,
    PRIMARY KEY (request_id, position)
);

CREATE INDEX ON access_request_steps (tenant_id, approver_id) WHERE status = 'pending';

-- The request an assignment came from.
ALTER TABLE access_assignments
    ADD COLUMN request_id uuid REFERENCES access_requests (id) ON DELETE SET NULL;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['access_requests', 'access_request_steps'] LOOP
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
