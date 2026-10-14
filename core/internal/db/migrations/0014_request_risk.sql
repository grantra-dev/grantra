-- A request that would break a prevent-mode separation of duties policy
-- waits on a "risk" step: an admin accepts the risk, with a reason (the
-- step's comment) and how many days the exception lasts.
ALTER TABLE access_request_steps DROP CONSTRAINT access_request_steps_kind_check;
ALTER TABLE access_request_steps ADD CONSTRAINT access_request_steps_kind_check
    CHECK (kind IN ('manager', 'owner', 'admin', 'risk'));
ALTER TABLE access_request_steps ADD COLUMN exception_days int CHECK (exception_days BETWEEN 1 AND 365);

-- Exceptions accepted on a request point to it.
ALTER TABLE sod_violations ADD COLUMN request_id uuid REFERENCES access_requests (id) ON DELETE SET NULL;
