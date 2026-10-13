-- Provisioning runs are recorded before they change anything and each
-- action as soon as the application confirms it, so a run that stops halfway
-- keeps what it did on record. A run is 'running' until it finishes, and
-- 'interrupted' when Grantra stopped before carrying out every action.
ALTER TABLE connector_runs DROP CONSTRAINT connector_runs_status_check;
ALTER TABLE connector_runs ADD CONSTRAINT connector_runs_status_check
    CHECK (status IN ('planned', 'running', 'succeeded', 'partial', 'failed', 'refused', 'interrupted'));

ALTER TABLE connector_runs ALTER COLUMN finished_at DROP NOT NULL;
ALTER TABLE connector_runs ADD CONSTRAINT connector_runs_finished_check
    CHECK ((status = 'running') = (finished_at IS NULL));

-- At most one run in progress per application.
CREATE UNIQUE INDEX connector_runs_one_running ON connector_runs (application_id) WHERE status = 'running';
