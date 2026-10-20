-- Non-human identities: service accounts, AI agents and workloads, each with
-- a person who answers for it (its manager_id).

-- Workloads join services and agents. A workload is a program identified by
-- a SPIFFE ID rather than by an account in an application.
ALTER TABLE identities DROP CONSTRAINT identities_kind_check;
ALTER TABLE identities ADD CONSTRAINT identities_kind_check CHECK (kind IN ('human', 'service', 'agent', 'workload'));

-- A non-human identity's manager_id is its owner: the person who answers
-- for it. They review its access and approve what it asks for, as a manager
-- does, and when they leave it passes to their own manager.

-- What a non-human identity is for, in a sentence.
ALTER TABLE identities ADD COLUMN purpose text CHECK (length(purpose) <= 500);

-- The SPIFFE ID a workload presents (spiffe://trust-domain/path), for
-- authorization checks from the workloads themselves.
ALTER TABLE identities ADD COLUMN spiffe_id text CHECK (spiffe_id ~ '^spiffe://[a-z0-9._-]+(/[A-Za-z0-9._~!$&''()*+,;=:@%-]+)*$');
CREATE UNIQUE INDEX identities_spiffe_id ON identities (tenant_id, spiffe_id) WHERE spiffe_id IS NOT NULL;
