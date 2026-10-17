# Credential vault

The vault keeps the passwords and SSH keys of privileged accounts (root on a database, a domain admin, a break-glass user), hands them to the people allowed to use them for a limited time, and changes the passwords in the target system so that a password someone saw stops working. To log in to servers without handing out keys at all, see [SSH access](ssh.md); to reach databases and Kubernetes clusters without anyone seeing the password, see [Databases and Kubernetes clusters](gateways.md).

## Turning it on

The vault seals secrets with a master key that the server reads from `GRANTRA_VAULT_KEY`: 32 random bytes in base64.

```sh
openssl rand -base64 32
```

- With Helm, set `vault.key` or point `vault.existingSecret` at a Secret with the key `vault-key` (see [production.md](production.md)).
- docker-compose sets a fixed demo key. Never use it for real secrets.
- Without the key the vault still lists and manages accounts, but cannot store, reveal or rotate secrets; the console says so.

**Keep a copy of the key somewhere safe and separate from the database backups.** Secrets sealed with a lost key cannot be recovered, and anyone holding both the key and a database backup can read them.

## Key providers

Instead of `GRANTRA_VAULT_KEY`, the master key can live in a key service, so that Grantra never holds it: a copy of the database or of Grantra's configuration then opens nothing without access to that key. Set `GRANTRA_VAULT_KEY_PROVIDER` and `GRANTRA_VAULT_KMS_KEY` (with Helm, `vault.provider` and `vault.kmsKey`) on the server, the [gateway](ssh.md#the-gateway) and wherever you run `grantra vault rewrap`.

| `GRANTRA_VAULT_KEY_PROVIDER` | `GRANTRA_VAULT_KMS_KEY` | Credentials |
|---|---|---|
| `local` (default) | | `GRANTRA_VAULT_KEY` |
| `aws-kms` | Key id, ARN or alias (`alias/grantra`) of a symmetric `ENCRYPT_DECRYPT` key | The AWS SDK's usual chain: IAM role for the service account (IRSA), instance profile, `AWS_*` variables. Needs `kms:Encrypt`, `kms:Decrypt` and `kms:DescribeKey`. |
| `gcp-kms` | `projects/P/locations/L/keyRings/R/cryptoKeys/K`, purpose `ENCRYPT_DECRYPT` | Application Default Credentials: workload identity, `GOOGLE_APPLICATION_CREDENTIALS`. Needs *Cloud KMS CryptoKey Encrypter/Decrypter* and `cloudkms.cryptoKeys.get`. |
| `azure-keyvault` | `https://<vault>.vault.azure.net/keys/<name>` (Key Vault or Managed HSM), an RSA key | `DefaultAzureCredential`: workload identity, managed identity, `AZURE_*` variables. The key must allow `wrapKey`, `unwrapKey` and `sign` (*Key Vault Crypto User*). |
| `transit` | The name of a transit key of type `aes256-gcm96`, `aes128-gcm96` or `chacha20-poly1305` | `GRANTRA_VAULT_TRANSIT_ADDR` (or `VAULT_ADDR`/`BAO_ADDR`), `GRANTRA_VAULT_TRANSIT_TOKEN` (or `VAULT_TOKEN`/`BAO_TOKEN`) or `GRANTRA_VAULT_TRANSIT_TOKEN_FILE`, re-read on every call so an agent can renew it; optional `_MOUNT` (default `transit`), `_NAMESPACE`, `_CACERT`. Needs `update` on `transit/encrypt/<key>`, `transit/decrypt/<key>` and `read` on `transit/keys/<key>`. At start Grantra checks that the server binds `associated_data` and refuses one that ignores it. |
| `pkcs11` | The label of an AES key in the HSM (the key id Grantra stores names the token too) | `GRANTRA_VAULT_PKCS11_MODULE` (the vendor's `.so`), `GRANTRA_VAULT_PKCS11_TOKEN` (token label), `GRANTRA_VAULT_PKCS11_PIN` or `GRANTRA_VAULT_PKCS11_PIN_FILE`. |

Grantra checks the key when it starts and refuses to start if it is missing, disabled or of the wrong kind.

Every provider binds a wrapped data key to the tenant, account and version it belongs to (the KMS's encryption context or additional authenticated data), so it does not open anywhere else. Azure Key Vault's wrap has no such field: Grantra wraps locally with the key's public half and has Key Vault sign the result together with the binding, and checks that signature before asking Key Vault to unwrap, so a wrapped key made without the vault's key is refused.

**HSMs.** The release binaries and image are static builds without PKCS#11, which needs cgo. Build Grantra with it:

```sh
cd core && CGO_ENABLED=1 go build -tags pkcs11 ./cmd/grantra
```

and run it where the vendor's PKCS#11 module is installed. CI tests it against SoftHSM.

**Rotating a key.** Rotating inside the provider (a new AWS, Google or Azure key version, `transit/keys/<key>/rotate`) needs nothing from Grantra: old data keys keep unwrapping, new ones use the new version. In Azure, pin a key version in the URL to keep it fixed; without one, Grantra wraps with the version current at start and unwraps with the version each data key names.

### Moving to another key or provider

1. Set the new provider and key, and keep the old one for unwrapping only: leave `GRANTRA_VAULT_KEY` set when moving from the local key, or set `GRANTRA_VAULT_OLD_KEY_PROVIDER` and `GRANTRA_VAULT_OLD_KMS_KEY` when moving from another provider or key, back to the local key included (the connection settings above are shared, so two transit keys must live in the same engine and two HSM keys in the same token). New secrets go to the new key.
2. Run `grantra vault rewrap` once, with the same environment (with Helm, `kubectl exec deploy/<release>-grantra -- /grantra vault rewrap` after the upgrade). It unwraps every data key the old key wrapped and wraps it with the new one, for vault secrets, the [SSH certificate authorities](ssh.md) and the [gateway's TLS CA](gateways.md#the-gateway); secrets themselves are not decrypted. It is safe to run again, and the audit log records it as `vault.rewrapped`.
3. Remove the old key's settings and restart. A secret still wrapped by a key Grantra no longer has fails to open with a message naming that key.

Each provider unwraps only with its own key (any version of it): a key id written into the database cannot make Grantra call another KMS key, vault or server with its credentials.

## How secrets are stored

Envelope encryption:
- Every version of every secret is encrypted with AES-256-GCM under its own random data key.
- The data key is wrapped (encrypted) by the master key, and only the wrapped data key is stored, with the id of the key that wrapped it (`local:` and the start of the key's SHA-256).
- Each ciphertext is bound to its tenant, vault account and version: copying it to another row makes it fail to open.

With an [external key provider](#key-providers) the master key never leaves the KMS or HSM: only it can unwrap a data key, and it logs every unwrap.

Secrets never appear in API lists, logs or the audit log. Only a check-out returns one, with `Cache-Control: no-store`.

## Who can use an account

Adding a vault account also adds an entitlement, **"Use \<name\>"**, to an application called **Privileged vault** (created the first time, governed by hand). Holding that entitlement, directly or through a business role, is what lets someone check the account out. So everything Grantra does with access applies:
- People [request](access-requests.md) it for a time, or to be eligible, and approvers approve it. Vault entitlements are always [just in time only](just-in-time.md): nobody holds them with no end date, and eligible people activate them for a few hours when needed.
- [Access reviews](access-reviews.md) cover it, and [separation of duties](separation-of-duties.md) policies can use it.
- It expires, and leavers lose it.

**Admins manage the vault but cannot read its secrets** unless they hold the access too, which they request like anyone else (and nobody approves their own request). Auditors see the accounts and their history, never a secret.

## Check-out

```sh
curl -b cookies -X POST localhost:8080/api/v1/vault/accounts/$ID/checkouts -d '{"reason": "INC-1234: restart the replica"}'
```

- A reason is required; it goes to the audit log (`vault.checked_out`).
- The check-out lasts the account's `checkout_minutes` (default 60, up to 480) and ends on its own (`vault.checkout_expired`), or earlier when the person checks it in. Admins can end anyone's check-out.
- Checking out again while yours is open replaces it with a fresh one.
- **Exclusive** accounts are checked out by one person at a time; the next person gets a `409` saying who has it until when.

## Break glass

For emergencies, an account can be marked **break glass**. Its
**responders** (people you list, or the Grantra admins when you list nobody
or every listed responder has left) can open it without holding its access.
Whoever last turned break glass on or changed its responders is never one of
them, so nobody arms an account for their own use: with a single admin, list
someone else.

```sh
curl -b cookies -X POST localhost:8080/api/v1/vault/accounts/$ID/break-glass -d '{"reason": "INC-9: production is down"}'
```

- A reason is required. The check-out lasts the account's `checkout_minutes`, at most 60, and ignores exclusivity: an emergency does not wait for whoever has it.
- Every admin and auditor gets an email at once (`break_glass_used`), which nobody can turn off (`vault.break_glass_opened` in the audit log).
- When the last check-out of the exposed password ends, Grantra rotates it if it can (the account is linked to an application whose connector sets passwords), even when the account does not rotate after each use.
- Every use waits for review in **Vault → Break glass**: an admin other than the person who used it marks it *justified* or *flags* it with a reason (`vault.break_glass_reviewed`). Admins get a reminder email every day until then (job **remind break-glass reviews**, every 15 minutes).
- Like any check-out, an ordinary one ends no later than the access it comes from, such as a [just-in-time](just-in-time.md) activation, and ends within a minute once that access is gone.

In the console, admins tick *Break glass* in the account's settings and add
responders; responders see a **Break glass** button next to the account.

## Rotation

Grantra changes a password in the target system through the account's connector. That needs the vault account linked to an aggregated account (`account_id`) in an application whose connector can set passwords:

| Connector | How |
|---|---|
| LDAP, Active Directory | Replaces `unicodePwd`. Active Directory only accepts it over TLS (`ldaps://` or StartTLS), and the service account needs the *Reset password* right on the users |
| LDAP, OpenLDAP | The Password Modify extended operation (RFC 3062), so the server hashes the password |
| Grantra Login | Keycloak's reset-password, permanent, then ends the user's sessions |

Policy, per account:
- `rotate_every_days`: rotate on a schedule (counted from the current secret).
- `rotate_after_checkin`: rotate once the last open check-out ends, checked in or expired.

Scheduled rotations wait until nobody has the account checked out. Admins can also **rotate now** (`POST .../rotations`), which does not wait.

Generated passwords are 24 characters with upper and lower case letters, digits and symbols, which meets Active Directory's complexity rules.

A rotation never loses a password: the new one is sealed and saved as a *pending* version before the target is changed, and becomes current only once the target accepts it. If the target refuses or cannot be reached, the old password stays current, the failure is recorded (`vault.rotation_failed`, and on the account), and the same pending password is tried again after 5 minutes, then 10, 20 and so on up to a day. An admin typing a new secret abandons a pending rotation. An account that can no longer rotate (its link or connector changed) stops retrying and shows why.

A background job runs every minute: it ends expired check-outs, then rotates what is due.

## Discovery

`GET /api/v1/vault/discovery` (and the Discovery tab) lists active accounts in the applications Grantra reads that probably belong in the vault:
- **Privileged**: they hold a high or critical risk entitlement.
- **Nobody's**: no person is linked to them (orphans).
- **Service**: they belong to an identity of kind `service`.

Accounts already in the vault are listed too, marked, after the ones still outside it. "Add to vault" pre-fills a new vault account linked to the found account.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/vault/accounts` | Everyone: admins and auditors see every account, others the ones they hold or respond to. `held` says whether you can check it out, `responder` whether you can break the glass; other people's check-out reasons show only to admins and auditors |
| `GET /api/v1/vault/accounts/{id}` | `vault.read` (admins, auditors): the account, its versions and latest check-outs |
| `POST /api/v1/vault/accounts` | `vault.manage` (admins): `{"name", "username", "kind": "password" \| "ssh_key", "risk": "critical" \| "high", "account_id", "secret", "address", "notes", "checkout_minutes", "exclusive", "rotate_every_days", "rotate_after_checkin", "break_glass", "break_glass_responders": [identity ids]}` |
| `PUT /api/v1/vault/accounts/{id}` | `vault.manage`: `address`, `notes`, `checkout_minutes`, `exclusive`, `rotate_every_days`, `rotate_after_checkin`, `break_glass`, `break_glass_responders` |
| `DELETE /api/v1/vault/accounts/{id}` | `vault.manage`. Removes its secrets, check-outs and its entitlement with every assignment and request of it |
| `PUT /api/v1/vault/accounts/{id}/secret` | `vault.manage`: `{"secret": "..."}`, becomes the current version |
| `POST /api/v1/vault/accounts/{id}/rotations` | `vault.manage`: `200` with `{"rotated": true, "version": 3}`, or `rotated: false` and the `error` |
| `POST /api/v1/vault/accounts/{id}/checkouts` | `vault_account.checkout`: only people holding the access, admins included. `201` with the secret |
| `POST /api/v1/vault/checkouts/{id}/checkin` | The person who checked it out, or `vault.manage` |
| `POST /api/v1/vault/accounts/{id}/break-glass` | `vault_account.break_glass`: the account's responders. `{"reason"}`, `201` with the secret |
| `GET /api/v1/vault/break-glass?status=pending\|all` | `vault.read` (admins, auditors) |
| `POST /api/v1/vault/break-glass/{id}/review` | `break_glass_use.review`: admins, never on their own use. `{"justified": true \| false, "comment"}`; flagging needs a comment |
| `GET /api/v1/vault/discovery?limit=` | `vault.read` |

Audit events: `vault.account_created`, `vault.account_updated`, `vault.account_deleted`, `vault.secret_set`, `vault.checked_out`, `vault.checked_in`, `vault.checkout_expired`, `vault.rotated`, `vault.rotation_failed`, `vault.break_glass_opened`, `vault.break_glass_reviewed`.
