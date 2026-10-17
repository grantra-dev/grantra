-- Indexes for lookups that read whole tables once they grow.

-- The audit log of one person: what they did and what was done to them.
CREATE INDEX audit_events_actor ON audit_events (tenant_id, actor, id DESC);
CREATE INDEX audit_events_target ON audit_events (tenant_id, target_type, target_id, id DESC);

-- Access with an end date, for the job that removes it when it ends
-- (activations have their own index, from 0017).
CREATE INDEX access_assignments_expiring ON access_assignments (tenant_id, expires_at)
    WHERE expires_at IS NOT NULL AND origin <> 'activation';

-- Sent and failed notifications, for the cleanup of old ones.
CREATE INDEX notification_outbox_done ON notification_outbox (tenant_id, created_at) WHERE status <> 'pending';

-- An application's account by user name, for apps asking Grantra whether
-- someone may do something.
CREATE INDEX accounts_username ON accounts (tenant_id, application_id, lower(username)) WHERE status <> 'deleted';

-- Searching gateway queries and SSH commands for any text. Trigram indexes
-- need the pg_trgm extension, which ships with PostgreSQL and which any
-- database owner can create; where it cannot be created, search still
-- works, only slower.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
    CREATE INDEX gateway_events_text ON gateway_events USING gin (text gin_trgm_ops);
    CREATE INDEX ssh_commands_command ON ssh_commands USING gin (command gin_trgm_ops);
EXCEPTION WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
    RAISE NOTICE 'pg_trgm is not available (%), so gateway searches use no index', SQLERRM;
END;
$$;
