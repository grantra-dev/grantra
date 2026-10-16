-- The credential vault: privileged accounts whose passwords or SSH keys
-- Grantra keeps encrypted, checks out to the people allowed to use them, and
-- rotates in the target system.
--
-- Who may use a vault account is an entitlement (in the "Privileged vault"
-- application), so requests, approvals, reviews, separation of duties and
-- expiry govern it like any other access.

CREATE TABLE vault_accounts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name             text NOT NULL,
    -- The login the secret belongs to, and where it is used.
    username         text NOT NULL,
    address          text,
    notes            text,
    kind             text NOT NULL CHECK (kind IN ('password', 'ssh_key')),
    -- The aggregated account this is, when Grantra reads its application:
    -- rotation changes the password through that application's connector.
    account_id       uuid REFERENCES accounts (id) ON DELETE SET NULL,
    entitlement_id   uuid NOT NULL REFERENCES entitlements (id) ON DELETE RESTRICT,
    checkout_minutes integer NOT NULL DEFAULT 60 CHECK (checkout_minutes BETWEEN 5 AND 480),
    -- Exclusive accounts are checked out by one person at a time.
    exclusive        boolean NOT NULL DEFAULT false,
    rotate_every_days    integer CHECK (rotate_every_days BETWEEN 1 AND 365),
    rotate_after_checkin boolean NOT NULL DEFAULT false,
    -- When the next rotation is due; NULL when none is.
    rotate_at        timestamptz,
    rotation_failures integer NOT NULL DEFAULT 0,
    last_rotation_error text,
    created_by       text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (entitlement_id)
);

CREATE INDEX vault_accounts_rotate_at ON vault_accounts (tenant_id, rotate_at) WHERE rotate_at IS NOT NULL;
CREATE UNIQUE INDEX vault_accounts_one_per_account ON vault_accounts (account_id) WHERE account_id IS NOT NULL;

-- Every value a vault account has had. Each version is sealed with
-- AES-256-GCM under its own data key; key_id names the key that wrapped the
-- data key. A pending version is a rotation in flight: saved before the
-- target system is changed, so a crash never loses a password the target
-- may already have.
CREATE TABLE vault_secret_versions (
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    vault_account_id uuid NOT NULL REFERENCES vault_accounts (id) ON DELETE CASCADE,
    version          integer NOT NULL CHECK (version > 0),
    status           text NOT NULL CHECK (status IN ('pending', 'current', 'retired', 'failed')),
    source           text NOT NULL CHECK (source IN ('set', 'rotation')),
    key_id           text NOT NULL,
    wrapped_key      bytea NOT NULL,
    nonce            bytea NOT NULL,
    ciphertext       bytea NOT NULL,
    created_by       text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (vault_account_id, version)
);

CREATE UNIQUE INDEX vault_secret_versions_one_current
    ON vault_secret_versions (vault_account_id) WHERE status = 'current';
CREATE UNIQUE INDEX vault_secret_versions_one_pending
    ON vault_secret_versions (vault_account_id) WHERE status = 'pending';

-- Who took a secret out, why, and until when.
CREATE TABLE vault_checkouts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    vault_account_id uuid NOT NULL REFERENCES vault_accounts (id) ON DELETE CASCADE,
    identity_id      uuid NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    version          integer NOT NULL,
    reason           text NOT NULL,
    checked_out_at   timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    ended_at         timestamptz,
    -- checked_in by the person, expired by the clock, or ended by an admin.
    ended_by         text CHECK (ended_by IN ('checked_in', 'expired', 'ended')),
    CHECK ((ended_at IS NULL) = (ended_by IS NULL))
);

CREATE INDEX vault_checkouts_open ON vault_checkouts (tenant_id, vault_account_id) WHERE ended_at IS NULL;
CREATE INDEX vault_checkouts_identity ON vault_checkouts (tenant_id, identity_id, checked_out_at DESC);
CREATE INDEX vault_checkouts_account ON vault_checkouts (vault_account_id, checked_out_at DESC);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['vault_accounts', 'vault_secret_versions', 'vault_checkouts'] LOOP
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
