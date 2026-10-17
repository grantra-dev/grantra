# Remote access: databases, clusters, desktops and clouds

People reach **PostgreSQL** and **MySQL** databases, **Kubernetes**
clusters, **Windows desktops** (RDP) and the **AWS**, **Azure** and
**Google Cloud** consoles from **Remote access** in the console, for only
as long as their access lasts. Nobody learns a password or a key: Grantra
logs in with an account kept in the [vault](vault.md). It logs every query
and cluster request, and records desktops.

- Databases and clusters: people connect their own tools (psql, DBeaver,
  kubectl) through the Grantra gateway with a short-lived **token**. See
  [Connect](#connect).
- Desktops open in a browser tab, through Grantra's server and Apache
  Guacamole's proxy. See [Desktops](#desktops-rdp).
- Cloud consoles open signed in: Grantra gives the person the role for the
  session and takes it back after. See [Cloud consoles](#cloud-consoles).

## How it fits together

- Each database, cluster, desktop or cloud you add is a **target**: a name,
  its address, and the vault account Grantra logs in with. Grantra creates
  the entitlement *Connect to the \<name\> database* (or *Use the \<name\>
  cluster*, *Use the \<name\> desktop*, *Open the \<name\> AWS console*
  and so on) in the **Remote access** application, so it is
  requested, approved, reviewed and expires like any access. It is
  [just in time only](just-in-time.md) by default: people are made eligible
  and activate it for a few hours.
- For a database or cluster, whoever holds the access gets a **token** in
  **Remote access**. It
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

In **Remote access**, click **Get token** on a database or cluster you hold.

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
2. In **Remote access**, **Add a target**: the kind, a
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

## Desktops (RDP)

People click **Connect** on a desktop they hold, and it opens in a new
browser tab, already logged in. Grantra's server reaches the desktop
through [guacd](https://guacamole.apache.org/), Apache Guacamole's proxy,
which speaks RDP to the desktop and draws it for the browser. Nothing is
installed on people's computers and the desktop's port never faces them.

1. Run guacd where it reaches the desktops on port 3389, and point Grantra's
   server at it with `GRANTRA_GUACD_ADDR` (`guacd:4822`). docker-compose
   runs it; with Helm, set `desktops.enabled=true`
   ([production.md](production.md#desktops)). Only Grantra should reach
   guacd: it does what it is told.
2. Put the Windows login Grantra uses in the [vault](vault.md) as a password
   account, with `DOMAIN\user` as the user name for a domain account.
3. In **Remote access**, **Add a target**: *Windows desktop (RDP)*, the
   address (`10.0.0.9`, default port 3389), that vault account, and how
   the clipboard works:
   - **Paste into the desktop only** (the default): people can paste text in,
     nothing comes out.
   - **Copy and paste both ways**.
   - **Off**.

   Pick its **security**: **Network Level Authentication** (the default,
   what Windows asks for), or **TLS only** for desktops without NLA, such
   as xrdp on Linux. Either way the connection is TLS: Grantra never uses
   the legacy RDP encryption, which would let someone in between read the
   password. **TLS, verified** checks the desktop's certificate against
   guacd's trusted CAs; **TLS, not verified** accepts any (most desktops
   have a self-signed one).
   A desktop that says *Security negotiation failed (wrong security type?)*
   does not do NLA: switch it to **TLS only**.

The screen is **recorded**, all of it, from the first frame: admins and
auditors play it back from **Sessions** (click **Recording**). Recordings
stay in Grantra's database, up to `GRANTRA_RDP_MAX_RECORDING_MB` (1024 MB)
per session; a session whose recording is full ends. Drives, printing,
audio and file transfer are off. As with the other targets, Grantra checks
access every 30 seconds and ends the session when it goes or an admin ends
it, and a session lasts at most the target's maximum (8 hours by default).

The browser tab talks to Grantra over a WebSocket at
`/api/v1/gateway/targets/{id}/rdp`. Grantra only accepts it from its own
pages, so `GRANTRA_PUBLIC_URL` must be the address people open; a proxy in
front of Grantra must pass WebSockets on (ingress-nginx does by default).

## Cloud consoles

People click **Open console** on a cloud they hold, and the console opens
signed in as them, with the target's role, until their access ends or the
target's maximum (8 hours by default). Grantra needs the person's email
(the one the cloud knows them by).

### AWS

Grantra assumes the target's IAM role for the person (the session is named
after their email, so CloudTrail shows who it was) and signs them in to the
console through AWS federation. People also get the session's credentials
for the AWS CLI. AWS sessions last 15 minutes to 12 hours (1 hour by
default here), and the role's **maximum session duration** must be at least
as long.

1. Create an IAM user for Grantra with an access key, allowed only to assume
   the roles people get:

   ```json
   { "Version": "2012-10-17", "Statement": [{ "Effect": "Allow", "Action": "sts:AssumeRole",
     "Resource": "arn:aws:iam::123456789012:role/ReadOnly" }] }
   ```

   It has to be an IAM user (not a role): AWS only signs in to the console
   with sessions from long-term keys.
2. Let it assume the role, in the role's trust policy:

   ```json
   { "Effect": "Allow", "Principal": { "AWS": "arn:aws:iam::123456789012:user/grantra" },
     "Action": "sts:AssumeRole" }
   ```

3. Put the key in the vault as a password account: the access key ID as the
   user name, the secret access key as the password.
4. Add the target: *AWS console*, the role's ARN as the address, that vault
   account.

AWS cannot end a console session early: when access ends, the session stays
signed in until it expires, and **End** is not offered. To cut one short,
use **Revoke active sessions** on the role in IAM. Keep AWS targets' hours
short for that reason.

### Azure

Grantra assigns the person's Microsoft Entra user (found by user principal
name, then by mail) the target's role on its scope, and removes the
assignment when the session ends, within 30 seconds. While someone has a
session, opening it again reuses it.

1. Register an app in Microsoft Entra with a client secret. Give it
   **Role Based Access Control Administrator** (or User Access
   Administrator) on the scope, ideally limited to the roles people get, and
   the Microsoft Graph application permission **User.Read.All**, with admin
   consent.
2. Put it in the vault as a password account: the client ID as the user
   name, the secret as the password.
3. Add the target: *Azure portal*, the scope as the address
   (`/subscriptions/<id>` or `/subscriptions/<id>/resourceGroups/<name>`),
   the role (`Reader`, or a role definition's ID), the tenant (its ID or
   domain) and that vault account.

Without a vault account, Grantra uses its own Azure identity (a managed or
workload identity, environment variables or the Azure CLI's login). Someone
who already holds the role there keeps it: Grantra only removes what it
assigned.

### Google Cloud

Grantra adds the person's Google account (their email) to the project's IAM
policy with the target's role and a condition that ends it when the session
does, and removes the binding when the session ends. Basic roles (owner,
editor, viewer) are refused: Google Cloud does not let them expire. Pick a
predefined or custom role.

1. Create a service account for Grantra with **Project IAM Admin**
   (`roles/resourcemanager.projectIamAdmin`) on the project, and a JSON key.
2. Put the key in the vault as a password account: any user name, the whole
   JSON as the password.
3. Add the target: *Google Cloud console*, the project ID as the address,
   the role (`roles/compute.viewer`) and that vault account.

Without a vault account, Grantra uses its own credentials (workload identity
or `GOOGLE_APPLICATION_CREDENTIALS`).

While a cloud target has sessions running, Grantra refuses to delete it or
to change its address, role or tenant: what the sessions granted is taken
back where it was granted.

The job **end cloud console sessions** (every 30 seconds) takes back what
the sessions granted once access ends or time runs out; when a cloud refuses,
it tries again on the next run and the session shows the error.

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

docker-compose runs it on `localhost` (8443, 15432, 13306), with guacd for
desktops, and the demo adds the database **shop-db** and the desktop
**office-pc** for Luis; see [demo.md](demo.md). Helm: see
[production.md](production.md#gateway).

## Logs

Admins and auditors see every session in **Remote access → Sessions**, with
its queries or requests, or a desktop's recording; opening either is in the
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
| `POST /api/v1/gateway/targets`, `PUT`/`DELETE /api/v1/gateway/targets/{id}` | `vault.manage` (admins). Fields: `name`, `kind` (`postgres`, `mysql`, `kubernetes`, `rdp`, `aws`, `azure`, `gcp`), `risk`, `address`, `database`, `tls_mode` (`verify`, `require`, `disable`), `ca_certificate`, `vault_account_id`, `kube_groups`, `clipboard` (`both`, `paste`, `none`), `rdp_security` (`nla`, `tls`), `cloud_role`, `cloud_directory`, `max_token_hours`, `notes`. A cloud with sessions running cannot be deleted, nor its address, role or tenant changed |
| `POST /api/v1/gateway/targets/{id}/tokens` | Only people holding the target's access, admins included: the token, and for a cluster the kubeconfig. Databases and clusters only |
| `GET /api/v1/gateway/targets/{id}/rdp?width=&height=&dpi=&timezone=` | People holding a desktop's access: the WebSocket (subprotocol `guacamole`) the desktop runs on |
| `POST /api/v1/gateway/targets/{id}/console` | People holding a cloud's access: the sign-in `url`, `valid_before`, and on AWS `credentials` for the CLI |
| `GET /api/v1/gateway/sessions?target=&identity=&active=true&limit=` | `vault.read` (admins, auditors) |
| `GET /api/v1/gateway/sessions/{id}?after=` | `vault.read`: the session and its log, 500 entries after the entry id `after` |
| `GET /api/v1/gateway/sessions/{id}/recording` | `vault.read`: a desktop session's recording, in the Guacamole protocol |
| `POST /api/v1/gateway/sessions/{id}/terminate` | `vault.manage`: ends it and revokes its token (not on AWS) |
| `GET /api/v1/gateway/events?q=&target=&limit=` | `vault.read`: searches queries and requests |
