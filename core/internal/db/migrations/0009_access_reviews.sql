-- Access reviews: a campaign asks reviewers (each person's manager, or the
-- owner of the application or role) to confirm that people still need the
-- access they were given by hand or by request. Revoked access is removed
-- right away, and provisioning takes it out of the application. Birthright
-- access is left out: rules give it, and rules are what to review for it.

CREATE TABLE review_campaigns (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name            text NOT NULL,
    reviewer        text NOT NULL CHECK (reviewer IN ('manager', 'owner')),
    -- Applications whose entitlements are reviewed; empty means everything,
    -- business roles included.
    application_ids uuid[] NOT NULL DEFAULT '{}',
    -- What happens to access nobody reviewed by due_at.
    on_missing      text NOT NULL DEFAULT 'keep' CHECK (on_missing IN ('keep', 'revoke')),
    due_at          timestamptz NOT NULL,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed')),
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    closed_at       timestamptz,
    UNIQUE (tenant_id, name),
    CHECK ((status = 'active') = (closed_at IS NULL))
);

-- One assignment to review. The access is copied so the item keeps its
-- meaning after the assignment is gone.
CREATE TABLE review_items (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    campaign_id    uuid NOT NULL REFERENCES review_campaigns (id) ON DELETE CASCADE,
    assignment_id  uuid REFERENCES access_assignments (id) ON DELETE SET NULL,
    identity_id    uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    role_id        uuid REFERENCES roles (id) ON DELETE CASCADE,
    entitlement_id uuid REFERENCES entitlements (id) ON DELETE CASCADE,
    origin         text NOT NULL,
    granted_at     timestamptz NOT NULL,
    -- NULL means any admin reviews it: nobody else to ask.
    reviewer_id    uuid REFERENCES identities (id) ON DELETE SET NULL,
    decision       text NOT NULL DEFAULT 'pending' CHECK (decision IN ('pending', 'keep', 'revoke')),
    decided_by     text,
    decided_at     timestamptz,
    comment        text,
    CHECK ((role_id IS NULL) <> (entitlement_id IS NULL)),
    CHECK ((decision = 'pending') = (decided_at IS NULL))
);

CREATE INDEX ON review_items (tenant_id, campaign_id);
CREATE INDEX ON review_items (tenant_id, reviewer_id) WHERE decision = 'pending';

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['review_campaigns', 'review_items'] LOOP
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
