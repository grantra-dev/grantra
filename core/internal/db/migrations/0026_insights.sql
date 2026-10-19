-- Attack paths and access insights: what Grantra needs to tell whether
-- access is used, and to compare people with their peers.

-- When the person last signed in to the application with this account, as
-- the application says (Active Directory's lastLogonTimestamp, an account
-- file's last_login column). NULL when the application does not say.
ALTER TABLE accounts ADD COLUMN last_login_at timestamptz;
CREATE INDEX accounts_last_login ON accounts (tenant_id, application_id) WHERE last_login_at IS NOT NULL;

-- When the eligibility was last turned on. Activations are deleted once they
-- end, so this is what says whether just-in-time access gets used.
ALTER TABLE access_eligibilities ADD COLUMN last_activated_at timestamptz;

-- Sign-ins to an application, by its name, for when someone last used it.
CREATE INDEX signin_events_application ON signin_events (tenant_id, lower(application), identity_id, occurred_at DESC)
    WHERE outcome = 'success' AND application IS NOT NULL;

-- A person's sessions through the gateway, for when they last used a target.
CREATE INDEX gateway_sessions_identity ON gateway_sessions (tenant_id, identity_id, started_at DESC);

-- A person's peers: the people in their department (or on their manager's
-- team, which 0003 indexes already).
CREATE INDEX identities_department ON identities (tenant_id, (attributes ->> 'department')) WHERE status = 'active';
