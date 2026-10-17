# Databases and Kubernetes clusters

People connect to **PostgreSQL** and **MySQL** databases and **Kubernetes**
clusters through the Grantra gateway with a **token** that lasts only as
long as their access. Nobody learns the database password or the cluster
credentials: the gateway logs in with an account kept in the
[vault](vault.md), and logs every query and cluster request.

## How it fits together

- Each database or cluster you add is a **target**: a name, its address, and
  the vault account the gateway logs in with. Grantra creates the
  entitlement *Connect to the \<name\> database* (or *Use the \<name\>
  cluster*) in the **Databases and clusters** application, so it is
  requested, approved, reviewed and expires like any access. It is
  [just in time only](just-in-time.md) by default: people are made eligible
  and activate it for a few hours.
- Whoever holds the access gets a **token** in **Databases & clusters**. It
  lasts the target's maximum (8 hours by default, 24 at most) and never past
  the access, and works only for that target. Grantra keeps only its hash.
- The token is the database password, or the bearer token in the
  kubeconfig the console hands out. Clients always talk **TLS** to the
  gateway, so it never travels in the clear.
- The gateway checks the token and the access when someone connects (on a
  cluster, on every request) and every 30 seconds after. When the access
  ends (an activation runs out, a review revokes it, the person leaves) or
  an admin clicks **End**, the session ends and its token stops working.

## Connect

In **Databases & clusters**, click **Get token** on a target you hold.

**PostgreSQL**: save the gateway's certificate (the button, or
`curl -o grantra-gateway-ca.pem https://grantra.example.com/api/v1/gateway/ca.pem`)
and connect with the target's name as the user and the token as the
password:

```sh
psql "host=gateway.example.com port=15432 user=orders-db sslmode=verify-full sslrootcert=grantra-gateway-ca.pem"
```

Any client works the same way (DBeaver, pgAdmin, an application's
connection string). `dbname` is optional: without it you land in the
target's database.

**MySQL** (or MariaDB):

```sh
mysql -h gateway.example.com -P 13306 -u billing -p --ssl-mode=VERIFY_IDENTITY --ssl-ca=grantra-gateway-ca.pem
```

**Kubernetes**: download the kubeconfig and use it:

```sh
kubectl --kubeconfig grantra-prod.yaml get pods
```

It points at `https://gateway.example.com:8443/kubernetes/prod` with the
token and the gateway's CA. `exec`, `logs -f`, `port-forward` and watches
work.

## Add a database

1. Create a **login for the gateway** on the database, with the rights
   people need there (often less than the owner's), and put its password in
   the [vault](vault.md) as a password account. Nobody needs to hold that
   vault account's access: the gateway opens it itself. Rotation in the
   vault does not reach databases yet; change the password on the database
   and then in the vault.
2. In **Databases & clusters**, **Add database or cluster**: the kind, a
   name (what people type as the user), the address (`10.0.0.7`, default
   port 5432 or 3306), the default database and that vault account.
3. Pick how the gateway talks to the database: **TLS, verified** (the
   default; paste the database's CA, or leave it empty for the system's),
   **TLS, not verified** or **No TLS** (only on a network you trust).
4. Make people eligible, or let them request it.

The **default database** is where people land when they name none; they can
still connect to another one the login may use. To keep someone to one
database, give the gateway's login rights on that one only.

## Add a Kubernetes cluster

The gateway reaches the cluster's API server with its own service account
and acts as the person through
[impersonation](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#user-impersonation):
the cluster sees the user `grantra:<email>` in the groups of the target
(`grantra:<target name>` unless you set others), so your RBAC decides what
they can do and the cluster's audit log names them.

1. On the cluster, create the gateway's service account, let it impersonate
   (and nothing else), and give the target's group the role people get:

   ```yaml
   apiVersion: v1
   kind: ServiceAccount
   metadata: { name: grantra-gateway, namespace: grantra }
   ---
   apiVersion: v1
   kind: Secret
   metadata:
     name: grantra-gateway-token
     namespace: grantra
     annotations: { kubernetes.io/service-account.name: grantra-gateway }
   type: kubernetes.io/service-account-token
   ---
   apiVersion: rbac.authorization.k8s.io/v1
   kind: ClusterRole
   metadata: { name: grantra-gateway-impersonate }
   rules:
     - apiGroups: [""]
       resources: [users, groups]
       verbs: [impersonate]
     - apiGroups: [authentication.k8s.io]
       resources: [userextras/grantra-identity, userextras/grantra-session]
       verbs: [impersonate]
   ---
   apiVersion: rbac.authorization.k8s.io/v1
   kind: ClusterRoleBinding
   metadata: { name: grantra-gateway-impersonate }
   roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: grantra-gateway-impersonate }
   subjects: [{ kind: ServiceAccount, name: grantra-gateway, namespace: grantra }]
   ---
   # What people with the target's access may do: here, edit in shop.
   apiVersion: rbac.authorization.k8s.io/v1
   kind: RoleBinding
   metadata: { name: grantra-prod-edit, namespace: shop }
   roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: edit }
   subjects: [{ kind: Group, name: "grantra:prod", apiGroup: rbac.authorization.k8s.io }]
   ```

2. Put the token in the vault as a password account
   (`kubectl -n grantra get secret grantra-gateway-token -o jsonpath='{.data.token}' | base64 -d`).
3. Add the cluster: its API server URL (`https://10.0.0.1:6443`), that vault
   account, and its CA (`kubectl config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d`).

Groups starting with `system:` are refused (`system:masters` would skip
RBAC). Whatever identity a client claims (`--as`, `Impersonate-*` headers)
is dropped.

For the cluster the gateway runs in, leave the vault account empty: the
gateway uses its own service account. With Helm, set
`gateway.kubernetes.inCluster=true`, which also creates the ClusterRole
above.

## The gateway

The same `grantra gateway` that runs the [SSH gateway](ssh.md#the-gateway)
also listens for databases and clusters. Besides the SSH settings:

| Variable | Default | Meaning |
|---|---|---|
| `GRANTRA_GATEWAY_KUBERNETES_ADDR`, `GRANTRA_GATEWAY_POSTGRES_ADDR`, `GRANTRA_GATEWAY_MYSQL_ADDR` | `:8443`, `:15432`, `:13306` | Where each listener listens; `off` turns one off |
| `GRANTRA_GATEWAY_TLS_NAMES` | the host name, `localhost`, `127.0.0.1` | Comma-separated names and IPs of its TLS certificate, which Grantra signs with the tenant's gateway CA (kept sealed with the vault key) and renews on its own |
| `GRANTRA_GATEWAY_TLS_CERT_FILE`, `GRANTRA_GATEWAY_TLS_KEY_FILE` | | A certificate of your own instead (both or neither), read again when the file changes |
| `GRANTRA_GATEWAY_KUBERNETES_ADDRESS`, `GRANTRA_GATEWAY_POSTGRES_ADDRESS`, `GRANTRA_GATEWAY_MYSQL_ADDRESS` (on the server) | the host of `GRANTRA_SSH_GATEWAY_ADDRESS` with the ports above | `host:port` people reach each listener at, shown in the console |
| `GRANTRA_GATEWAY_CA_FILE` (on the server) | | With your own certificate, the PEM CA clients verify it with, which the console hands out |

docker-compose runs it on `localhost` (8443, 15432, 13306) and the demo adds
the database **shop-db** for Luis; see [demo.md](demo.md). Helm: see
[production.md](production.md#gateway).

## Logs

Admins and auditors see every session in **Databases & clusters →
Sessions**, with its queries or requests; reading a session's log is in the
audit log. **Queries** searches them all: type `DELETE FROM` and see who ran
it, where and when.

- **PostgreSQL** logs each simple query and each statement prepared
  (`$1` stays as is: parameter values are not logged), and fast-path
  function calls.
- **MySQL** logs each query, each statement prepared (with `?`) and each
  change of database.
- **Kubernetes** logs each request (`DELETE /api/v1/namespaces/shop/pods/web`)
  with what the cluster answered, but not API discovery, which kubectl asks
  for all the time. What goes on inside `kubectl exec` or `port-forward` is
  not logged.

Each entry keeps its first 4,000 characters. A session that reaches 100,000
entries ends, so nothing runs unlogged; connecting again starts a new one.
A cluster session is a run of requests with one token, ending after 10
minutes without any.

The gateway refuses what it could not log or check: PostgreSQL replication
connections, MySQL `LOAD DATA LOCAL` (the file would go unlogged), logging
in again as another database user on the same connection
(`COM_CHANGE_USER`), and compression. In the `mysql` client, Ctrl+C does
not stop a running query: end it with `KILL QUERY` from another session.

A session whose gateway stops reporting (it crashed) is marked *lost*
within a few minutes by the job **end lost gateway sessions**.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/gateway` | Everyone: whether the gateway is on, its addresses and CA certificate |
| `GET /api/v1/gateway/ca.pem` | Anyone, without signing in: the CA certificate clients verify the gateway with |
| `GET /api/v1/gateway/targets` | Admins and auditors see all; everyone else the targets they hold, without the vault account |
| `POST /api/v1/gateway/targets`, `PUT`/`DELETE /api/v1/gateway/targets/{id}` | `vault.manage` (admins). Fields: `name`, `kind` (`postgres`, `mysql`, `kubernetes`), `risk`, `address`, `database`, `tls_mode` (`verify`, `require`, `disable`), `ca_certificate`, `vault_account_id`, `kube_groups`, `max_token_hours`, `notes` |
| `POST /api/v1/gateway/targets/{id}/tokens` | Only people holding the target's access, admins included: the token, and for a cluster the kubeconfig |
| `GET /api/v1/gateway/sessions?target=&identity=&active=true&limit=` | `vault.read` (admins, auditors) |
| `GET /api/v1/gateway/sessions/{id}?after=` | `vault.read`: the session and its log, 500 entries after the entry id `after` |
| `POST /api/v1/gateway/sessions/{id}/terminate` | `vault.manage`: ends it and revokes its token |
| `GET /api/v1/gateway/events?q=&target=&limit=` | `vault.read`: searches queries and requests |
