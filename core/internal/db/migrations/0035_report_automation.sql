-- Evidence packs Grantra makes on its own (Governance → Reports → Scheduled):
-- one report or one framework, for each period of so many months as it
-- ends, for the people it goes to (admins and auditors, who get an email).
CREATE TABLE report_schedules (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- Exactly one of report and framework.
    report         text,
    framework      text,
    application_id uuid REFERENCES applications (id) ON DELETE CASCADE,
    months         integer NOT NULL CHECK (months BETWEEN 1 AND 12),
    recipients     uuid[] NOT NULL CHECK (cardinality(recipients) BETWEEN 1 AND 50),
    enabled        boolean NOT NULL DEFAULT true,
    created_by     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK ((report IS NULL) <> (framework IS NULL)),
    CHECK (application_id IS NULL OR report IS NOT NULL)
);

-- Each pack a schedule made, kept for the recipients to download (for as
-- long as Settings → General → Compliance reports says). A schedule makes
-- one per period; one that could not be made keeps why.
CREATE TABLE report_deliveries (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    schedule_id uuid REFERENCES report_schedules (id) ON DELETE SET NULL,
    title       text NOT NULL,
    period_from date NOT NULL,
    period_to   date NOT NULL,
    status      text NOT NULL CHECK (status IN ('ready', 'failed')),
    error       text,
    file_name   text,
    data        bytea,
    sha256      text,
    recipients  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (schedule_id, period_from, period_to),
    CHECK (status = 'failed' OR data IS NOT NULL)
);
CREATE INDEX report_deliveries_created ON report_deliveries (tenant_id, created_at);

-- Who held what, and when: each membership an application reported (an
-- active account holding an entitlement), from when Grantra saw it until
-- it went. Grantra records the changes every few minutes, so "who had
-- access to X on 31 March" has an answer after the fact.
CREATE TABLE access_history (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    account_id     uuid NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    entitlement_id uuid NOT NULL REFERENCES entitlements (id) ON DELETE CASCADE,
    -- Whose account it was then; NULL for an account nobody owned.
    identity_id    uuid REFERENCES identities (id) ON DELETE SET NULL,
    held_from      timestamptz NOT NULL,
    -- NULL while it is still held.
    held_until     timestamptz,
    recorded_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX access_history_open ON access_history (account_id, entitlement_id) WHERE held_until IS NULL;
CREATE INDEX access_history_membership ON access_history (account_id, entitlement_id, held_until);
CREATE INDEX access_history_period ON access_history (tenant_id, held_from, held_until);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['report_schedules', 'report_deliveries', 'access_history'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid)$p$, t);
    END LOOP;
END $$;

-- External auditors are people invited from Reports, not from HR. The
-- grantra_guest attribute marks them, and birthright rules never match it
-- unless they name it.
CREATE INDEX identities_external_auditors ON identities (tenant_id) WHERE attributes->>'grantra_guest' = 'auditor';
