-- Identity lifecycle: authoritative sources (HR) and the joiner / mover /
-- leaver events they produce.
--
-- An identity source is where Grantra learns who works at the company: an HR
-- export (CSV today; Workday, BambooHR... later). Each sync compares the feed
-- with the identities the source owns and turns the differences into
-- lifecycle events. Later stages (birthright access, deprovisioning) consume
-- those events, so lifecycle_events doubles as an outbox: processed_at stays
-- NULL until something has acted on the event.

CREATE TABLE identity_sources (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('csv')),
    config     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- The source that owns the identity (NULL for identities created by hand or
-- by the bootstrap), and the dates HR gives for the job. start_date in the
-- future keeps the identity pending; an end_date in the past terminates it.
ALTER TABLE identities
    ADD COLUMN source_id  uuid REFERENCES identity_sources (id) ON DELETE SET NULL,
    ADD COLUMN start_date date,
    ADD COLUMN end_date   date;

-- One sync of a source: what it found and whether it was applied.
CREATE TABLE identity_source_runs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    source_id  uuid NOT NULL REFERENCES identity_sources (id) ON DELETE CASCADE,
    actor      text NOT NULL,
    dry_run    boolean NOT NULL,
    -- planned: dry run; applied: changes written; invalid: the file had
    -- errors; refused: a safety limit stopped it (too many leavers).
    status     text NOT NULL CHECK (status IN ('planned', 'applied', 'invalid', 'refused')),
    counts     jsonb NOT NULL DEFAULT '{}'::jsonb,
    errors     jsonb NOT NULL DEFAULT '[]'::jsonb,
    warnings   jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE lifecycle_events (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    identity_id   uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('joiner', 'mover', 'leaver', 'rehire')),
    run_id        uuid REFERENCES identity_source_runs (id) ON DELETE SET NULL,
    changes       jsonb NOT NULL DEFAULT '{}'::jsonb,
    effective_at  timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    processed_at  timestamptz
);

CREATE INDEX ON identities (tenant_id, source_id);
CREATE INDEX ON identities (tenant_id, status, start_date) WHERE status = 'pending';
CREATE INDEX ON identities (tenant_id, end_date) WHERE end_date IS NOT NULL;
CREATE INDEX ON identity_source_runs (tenant_id, source_id, created_at DESC);
CREATE INDEX ON lifecycle_events (tenant_id, identity_id);
CREATE INDEX ON lifecycle_events (tenant_id, id) WHERE processed_at IS NULL;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['identity_sources', 'identity_source_runs', 'lifecycle_events'] LOOP
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
