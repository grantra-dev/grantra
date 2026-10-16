-- Just-in-time access, zero standing privileges and break-glass.
--
-- People are made *eligible* for access and activate it for a few hours
-- when they need it. Activating gives an assignment with origin
-- 'activation' that always ends. Entitlements say how long an activation may
-- last, whether it needs an approval, and whether the entitlement is
-- just-in-time only: then nobody holds it without an end date.

ALTER TABLE entitlements
    ADD COLUMN jit_only boolean NOT NULL DEFAULT false,
    ADD COLUMN max_activation_hours integer NOT NULL DEFAULT 4 CHECK (max_activation_hours BETWEEN 1 AND 24),
    ADD COLUMN activation_approval boolean NOT NULL DEFAULT false;

-- Vault access is just-in-time only from now on.
UPDATE entitlements SET jit_only = true WHERE id IN (SELECT entitlement_id FROM vault_accounts);

-- Who may activate what. expires_at ends the eligibility itself.
CREATE TABLE access_eligibilities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    granted_by     text NOT NULL,
    granted_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz,
    request_id     uuid REFERENCES access_requests (id) ON DELETE SET NULL,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL))
);

CREATE UNIQUE INDEX access_eligibilities_unique
    ON access_eligibilities (identity_id, coalesce(role_id, entitlement_id));
CREATE INDEX ON access_eligibilities (tenant_id, identity_id);

ALTER TABLE access_assignments DROP CONSTRAINT access_assignments_origin_check;
ALTER TABLE access_assignments ADD CONSTRAINT access_assignments_origin_check
    CHECK (origin IN ('birthright', 'manual', 'request', 'activation'));
ALTER TABLE access_assignments
    ADD COLUMN eligibility_id uuid REFERENCES access_eligibilities (id) ON DELETE SET NULL,
    ADD COLUMN justification text,
    ADD CONSTRAINT access_assignments_activation_ends CHECK (origin <> 'activation' OR expires_at IS NOT NULL);
CREATE INDEX access_assignments_activations ON access_assignments (tenant_id, expires_at) WHERE origin = 'activation';

-- access_is_jit_only says whether nobody may hold a role or entitlement
-- without an end date: a just-in-time only entitlement, or a role that
-- includes one.
CREATE FUNCTION access_is_jit_only(role uuid, entitlement uuid) RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM entitlements WHERE id = entitlement AND jit_only)
        OR EXISTS (SELECT 1 FROM role_entitlements re JOIN entitlements e ON e.id = re.entitlement_id
                   WHERE re.role_id = role AND e.jit_only)
$$;

-- The last line of defence: whatever path writes an assignment, standing
-- access to just-in-time only access is refused. Grantra checks first and
-- explains; this only fires on a bug.
CREATE FUNCTION access_assignments_no_standing_jit() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.expires_at IS NULL AND access_is_jit_only(NEW.role_id, NEW.entitlement_id) THEN
        RAISE EXCEPTION 'just-in-time only access cannot be assigned without an end date'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER access_assignments_no_standing_jit
    BEFORE INSERT OR UPDATE OF expires_at ON access_assignments
    FOR EACH ROW EXECUTE FUNCTION access_assignments_no_standing_jit();

-- Requests ask for access, for eligibility, or to activate an eligibility
-- that needs approval. Durations can be in hours as well as days.
ALTER TABLE access_requests
    ADD COLUMN kind text NOT NULL DEFAULT 'access' CHECK (kind IN ('access', 'eligibility', 'activation')),
    ADD COLUMN duration_hours integer CHECK (duration_hours BETWEEN 1 AND 24),
    ADD CONSTRAINT access_requests_one_duration CHECK (duration_days IS NULL OR duration_hours IS NULL),
    ADD CONSTRAINT access_requests_activation_hours CHECK (kind <> 'activation' OR duration_hours IS NOT NULL),
    ADD CONSTRAINT access_requests_eligibility_days CHECK (kind <> 'eligibility' OR duration_hours IS NULL);

-- Applications whose access changed through just-in-time activations, for
-- the next provisioning pass to carry over at once instead of waiting for
-- the regular one.
CREATE TABLE jit_provisioning_queue (
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    queued_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id)
);

-- Break-glass: a vault account opened in an emergency without approval. The
-- responders may open it; with none listed, the platform admins may.
ALTER TABLE vault_accounts ADD COLUMN break_glass boolean NOT NULL DEFAULT false;

CREATE TABLE vault_break_glass_responders (
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    vault_account_id uuid NOT NULL REFERENCES vault_accounts (id) ON DELETE CASCADE,
    identity_id      uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    PRIMARY KEY (vault_account_id, identity_id)
);

-- Every break-glass check-out is reviewed afterwards by an admin other than
-- the person who used it.
ALTER TABLE vault_checkouts
    ADD COLUMN break_glass boolean NOT NULL DEFAULT false,
    ADD COLUMN review_status text CHECK (review_status IN ('pending', 'justified', 'flagged')),
    ADD COLUMN reviewed_by text,
    ADD COLUMN reviewed_at timestamptz,
    ADD COLUMN review_comment text,
    ADD COLUMN reminded_at timestamptz,
    ADD CONSTRAINT vault_checkouts_break_glass_review CHECK (break_glass = (review_status IS NOT NULL)),
    ADD CONSTRAINT vault_checkouts_reviewed CHECK ((review_status IN ('justified', 'flagged')) = (reviewed_at IS NOT NULL));
CREATE INDEX vault_checkouts_to_review ON vault_checkouts (tenant_id, checked_out_at) WHERE review_status = 'pending';

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['access_eligibilities', 'jit_provisioning_queue', 'vault_break_glass_responders'] LOOP
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
