-- Approval policies: who approves what is asked for, set up by admins in the
-- console (Governance → Approval flows). Policies are tried in position
-- order and the first enabled one whose conditions match a request decides
-- its steps; with none, the built-in chain (manager, then owner) does. The
-- conditions and steps are defined in package approvals.
CREATE TABLE approval_policies (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text,
    position    integer NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    conditions  jsonb NOT NULL DEFAULT '{}',
    steps       jsonb NOT NULL DEFAULT '[]',
    -- Goes up on every change; requests remember the version they followed.
    version     integer NOT NULL DEFAULT 1,
    updated_by  text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX ON approval_policies (tenant_id, position);

-- The policy a request followed, copied when it was made: changing the
-- policy later does not change requests already open.
ALTER TABLE access_requests
    ADD COLUMN policy_id      uuid REFERENCES approval_policies (id) ON DELETE SET NULL,
    ADD COLUMN policy_name    text,
    ADD COLUMN policy_version integer;

-- A step can ask several people: any one of them, a number of them, or all
-- (required). An empty approver_ids means any admin decides. A step not
-- decided by due_at expires the request, rejects it, or is escalated once to
-- the approvers' managers (on_timeout).
ALTER TABLE access_request_steps
    ADD COLUMN approver_ids   uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN required       integer NOT NULL DEFAULT 1 CHECK (required >= 1),
    ADD COLUMN label          text,
    ADD COLUMN timeout_hours  integer CHECK (timeout_hours BETWEEN 1 AND 720),
    ADD COLUMN on_timeout     text NOT NULL DEFAULT 'expire' CHECK (on_timeout IN ('expire', 'escalate', 'reject')),
    ADD COLUMN escalated_from uuid[],
    ADD COLUMN escalated_at   timestamptz;

UPDATE access_request_steps SET approver_ids = ARRAY[approver_id] WHERE approver_id IS NOT NULL;

-- Dropping the column drops its index of pending steps too.
ALTER TABLE access_request_steps DROP COLUMN approver_id;
CREATE INDEX ON access_request_steps USING gin (approver_ids) WHERE status = 'pending';

ALTER TABLE access_request_steps DROP CONSTRAINT access_request_steps_kind_check;
ALTER TABLE access_request_steps ADD CONSTRAINT access_request_steps_kind_check
    CHECK (kind IN ('manager', 'owner', 'people', 'holders', 'admin', 'risk'));

-- Who approved a step that needs more than one approval, and the one
-- approval of steps that need just one.
CREATE TABLE access_request_approvals (
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    request_id  uuid NOT NULL,
    position    integer NOT NULL,
    identity_id uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    comment     text,
    approved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, position, identity_id),
    FOREIGN KEY (request_id, position) REFERENCES access_request_steps (request_id, position) ON DELETE CASCADE
);

INSERT INTO access_request_approvals (tenant_id, request_id, position, identity_id, comment, approved_at)
SELECT s.tenant_id, s.request_id, s.position, i.id, s.comment, s.decided_at
FROM access_request_steps s
JOIN identities i ON s.decided_by = 'identity:' || i.id::text
WHERE s.status = 'approved' AND s.decided_at IS NOT NULL;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['approval_policies', 'access_request_approvals'] LOOP
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
