# Running Grantra in production

The Helm chart in [`deploy/helm/grantra`](../deploy/helm/grantra) runs Grantra on Kubernetes. The docker-compose stack in `deploy/` is for development only.

## What the chart runs

| Part | Default | Notes |
|---|---|---|
| Grantra core | on, 2 replicas | The API, the console and the background jobs (HR syncs, connector aggregation and provisioning, reminders, separation of duties scans). Connectors run inside the core, so there is no separate worker to deploy. |
| Grantra Login | off | The bundled Keycloak, for companies without an IdP. Leave it off when people sign in through Entra ID, Okta or similar and set `oidc.issuer`. |
| PostgreSQL | off | One in-cluster instance for trying Grantra out. Use a managed or separately run PostgreSQL with backups for real data. |

## Two database roles

Grantra keeps tenants apart with PostgreSQL row-level security. A role that owns a table can turn its row-level security off, so the server never connects as the owner:

| Role | Used by | Permissions |
|---|---|---|
| `grantra_owner` (any name) | `grantra migrate` | Owns the schema and every table. |
| `grantra_app` (any name) | the server | `SELECT`, `INSERT`, `UPDATE`, `DELETE` on the tables and usage of the sequences, granted by `grantra migrate` after each migration. Not a superuser, no `BYPASSRLS`. |

Create them once on your PostgreSQL:

```sql
CREATE ROLE grantra_owner LOGIN PASSWORD '...';
CREATE ROLE grantra_app LOGIN PASSWORD '...' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE grantra OWNER grantra_owner;
\connect grantra
ALTER SCHEMA public OWNER TO grantra_owner;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
```

Then give the chart both connection strings, either as values or, better, in a Secret you manage:

```sh
kubectl create secret generic grantra-db \
  --from-literal=database-url='postgres://grantra_app:...@db.example.com:5432/grantra?sslmode=verify-full' \
  --from-literal=migrate-database-url='postgres://grantra_owner:...@db.example.com:5432/grantra?sslmode=verify-full'
```

Each pod runs `grantra migrate` in an init container with the owner's connection string, which only that container sees. The server itself starts with `GRANTRA_MIGRATE_ON_START=false` and logs a warning if its role is a superuser, bypasses row-level security or owns tables.

Outside Kubernetes the same split works with the binary or image: run `grantra migrate` with `GRANTRA_DATABASE_URL` and `GRANTRA_MIGRATE_DATABASE_URL` set before each upgrade, and start the server with `GRANTRA_MIGRATE_ON_START=false`.

### Moving an existing install to two roles

If the server's role owns the tables today (as in the development stack), hand them to a new owner role once, as a superuser:

```sql
CREATE ROLE grantra_owner LOGIN PASSWORD '...';
REASSIGN OWNED BY grantra_app TO grantra_owner;  -- run in the grantra database
```

then run `grantra migrate` with both connection strings to grant `grantra_app` its data permissions.

## Several replicas

Every replica runs the background jobs on their timers. Before each pass a replica takes a PostgreSQL advisory lock named after the job (`pg_try_advisory_lock`); if another replica holds it, this replica skips the pass. So an aggregation or provisioning run never happens twice at once, whatever the replica count. The locks live on one extra connection per replica; if it drops, PostgreSQL releases them and the next pass reconnects.

## Install

```sh
helm install grantra deploy/helm/grantra \
  --set publicURL=https://grantra.example.com \
  --set database.existingSecret=grantra-db \
  --set oidc.issuer=https://login.microsoftonline.com/<tenant>/v2.0 \
  --set oidc.existingSecret=grantra-oidc \
  --set ingress.enabled=true,ingress.host=grantra.example.com
```

To try everything inside the cluster, with Grantra Login and the bundled PostgreSQL:

```sh
helm install grantra deploy/helm/grantra \
  --set publicURL=https://grantra.example.com,ingress.enabled=true,ingress.host=grantra.example.com \
  --set login.enabled=true,login.hostname=https://login.example.com,login.ingress.enabled=true,login.ingress.host=login.example.com \
  --set login.admin.password=...,oidc.clientSecret=... \
  --set postgresql.enabled=true \
  --set postgresql.auth.superuserPassword=...,postgresql.auth.ownerPassword=...,postgresql.auth.appPassword=...,postgresql.auth.loginPassword=...
```

Grantra Login starts empty: create the `grantra` realm and the `grantra-console` client (redirect URI `https://grantra.example.com/auth/callback`), or import [`deploy/keycloak/grantra-realm.json`](../deploy/keycloak/grantra-realm.json) after replacing its development secrets.

The [credential vault](vault.md) needs a master key: `--set vault.existingSecret=grantra-vault` with a Secret holding `vault-key` (from `openssl rand -base64 32`), or `vault.key`. Back the key up apart from the database: secrets sealed with a lost key are gone. To keep the master key in AWS KMS, Google Cloud KMS, Azure Key Vault, OpenBao or HashiCorp Vault Transit, or an HSM instead, set `vault.provider` and `vault.kmsKey`, give the pods their cloud identity with `serviceAccount.annotations` (plus `podLabels` `azure.workload.identity/use: "true"` for Azure), or pass credentials in `vault.env` and files (a token, a CA, an HSM PIN or module) with `vault.volumes` and `vault.volumeMounts`, and see [Key providers](vault.md#key-providers), including how to move an existing vault with `grantra vault rewrap`.

### Gateway

The gateway ([SSH](ssh.md), [databases and clusters](gateways.md)) runs as its own Deployment with `--set gateway.enabled=true` (it needs the vault key). Expose it with the Service it gets (`gateway.service.type`, `LoadBalancer` by default: port 2222 for SSH, and `gateway.ports` 8443, 15432 and 13306 for Kubernetes, PostgreSQL and MySQL, `0` turning one off) and set `gateway.publicAddress` to how people reach it, such as `gateway.example.com:2222`, so the console shows the right commands (`gateway.publicAddresses.*` when a listener is reached elsewhere). Its TLS certificate comes from the tenant's gateway CA for `gateway.tls.names` (by default the public host and the Service's names); bring your own with `gateway.tls.existingSecret` (a Secret with `tls.crt`, `tls.key` and `ca.crt`, such as cert-manager makes). `gateway.kubernetes.inCluster=true` lets clusters added without a vault account use the gateway's service account on its own cluster, with a ClusterRole that may only impersonate people. The gateway needs to reach the servers, databases and API servers behind it. Its host key is created once and kept in the Secret `<release>-grantra-gateway-host-key` across upgrades and uninstalls; bring your own with `gateway.hostKey.existingSecret` (key `host-key`). Do that when Argo CD, Flux or `helm template` render the chart: they cannot read the Secret back, so each render would make a new host key and every client would warn.

Connector credentials (`GRANTRA_SECRET_*`, see [connectors](connectors.md)) go in a Secret listed under `envFromSecrets`. Every other setting is in [`values.yaml`](../deploy/helm/grantra/values.yaml).
