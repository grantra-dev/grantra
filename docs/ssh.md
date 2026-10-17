# SSH access

People log in to servers with a **certificate** Grantra signs for them,
valid only while their access lasts. No personal keys live on the servers,
nothing has to be cleaned up when someone leaves, and sessions through the
**gateway** are recorded and searchable.

## How it fits together

- Grantra keeps two SSH **certificate authorities** (CAs) per tenant. The
  *people* CA signs people's certificates; the *gateway* CA signs only the
  gateway's own. Their private keys are sealed with the [vault](vault.md)
  key (`GRANTRA_VAULT_KEY` or a [key provider](vault.md#key-providers)), so
  SSH certificates need that key too.
- Each server trusts one of them (`TrustedUserCAKeys`), once: servers
  behind the gateway the gateway CA, so nobody logs in to them around it;
  servers reached directly the people CA.
- Each server you add is a **target**: a name, an address and the account to
  log in as. Grantra creates the entitlement *Log in to \<name\> as \<login\>*
  in the **SSH servers** application, so it is requested, approved, reviewed
  and expires like any access. It is [just in time only](just-in-time.md) by
  default: people are made eligible and activate it for a few hours.
- Whoever holds the access signs a certificate for their own public key in
  **Servers**. It lasts the target's maximum (8 hours by default, 24 at most)
  and never past the access, so an activation of 2 hours gives a certificate
  of 2 hours. Its principal is `grantra:<target name>`, which is never an
  account name: it only works where it was meant to.
- Targets are reached **through the gateway** (the default), which records
  every session, or **directly**, where the certificate logs in to the server
  itself and nothing is recorded.

## Set up a server

Once per server, as root. For a server **behind the gateway** (the
default), trust the gateway CA:

```sh
curl -o /etc/ssh/grantra_ca.pub https://grantra.example.com/api/v1/ssh/gateway_ca.pub
echo 'TrustedUserCAKeys /etc/ssh/grantra_ca.pub' >> /etc/ssh/sshd_config
systemctl reload sshd
```

Only the gateway holds certificates from that CA, two minutes at a time,
so people's own certificates do not work on the server.

For a server **reached directly**, trust the people CA and say which
targets may log in as each account, in an `AuthorizedPrincipalsFile`. With
the target `prod-db` logging in as `postgres`:

```sh
curl -o /etc/ssh/grantra_ca.pub https://grantra.example.com/api/v1/ssh/ca.pub
mkdir -p /etc/ssh/grantra_principals
echo 'grantra:prod-db' >> /etc/ssh/grantra_principals/postgres
cat >> /etc/ssh/sshd_config <<'CONF'
TrustedUserCAKeys /etc/ssh/grantra_ca.pub
AuthorizedPrincipalsFile /etc/ssh/grantra_principals/%u
CONF
systemctl reload sshd
```

A certificate for another target is refused there, and without the
principals file every certificate is (no account is called
`grantra:...`). Both keys are also shown on the **Servers** page for
admins. The account the target logs in as (`postgres`, `deploy`…) must
exist on the server.

When a target moves from direct to the gateway, switch the server to the
gateway CA too: that is also what stops the certificates people already
hold for it.

Then add the target in **Servers → Add server**, or:

```sh
curl -b cookies -X POST localhost:8080/api/v1/ssh/targets \
  -d '{"name": "prod-db", "login": "postgres", "address": "10.0.0.5:22", "risk": "critical"}'
```

| Setting | Meaning |
|---|---|
| `name` | What people type to connect: `ssh prod-db@gateway`. Lowercase letters, digits, `.`, `-`, `_` |
| `login` | The account on the server. Fixed once created: it is what the access means |
| `address` | `host:port` as the gateway reaches it (port 22 when left out) |
| `risk` | `high` (default) or `critical` |
| `max_certificate_hours` | 1 to 24 (default 8) |
| `via_gateway` | `true` (default): through the gateway, recorded. `false`: straight to the server, not recorded |

## Connect

1. Get the access: activate it from **My access**, or request it.
2. In **Servers**, click **Get certificate** and paste your public key (the
   contents of `~/.ssh/id_ed25519.pub`; Ed25519, ECDSA, RSA of 2048 bits or
   more, and FIDO `sk-` keys work).
3. Save the certificate next to the key, as `~/.ssh/id_ed25519-cert.pub`.
   `ssh` picks it up on its own.
4. Connect with the command the page shows:

```sh
ssh -p 2222 prod-db@ssh.example.com     # through the gateway
ssh postgres@10.0.0.5                   # a direct target
```

Commands work too: `ssh -p 2222 prod-db@ssh.example.com uptime`.

## The gateway

`grantra gateway` is the same binary in another mode. It needs the
database, the vault key and its own host key:

| Variable | Default | Meaning |
|---|---|---|
| `GRANTRA_DATABASE_URL`, `GRANTRA_TENANT`, `GRANTRA_VAULT_KEY` or the [key provider](vault.md#key-providers)'s settings | | The same as the server's |
| `GRANTRA_GATEWAY_ADDR` | `:2222` | Where it listens |
| `GRANTRA_GATEWAY_HOST_KEY_FILE` | `gateway_host_key` | Its SSH host key, created the first time. Keep it: a new one makes every client warn |
| `GRANTRA_SSH_GATEWAY_ADDRESS` (on the server) | | `host:port` people use to reach the gateway, shown in the console |

docker-compose runs it on `localhost:2222`; Helm with `gateway.enabled=true`
(see [production.md](production.md#ssh-gateway)). Its log prints the host
key fingerprint when it starts, for people to check the first time they
connect.

What it does:

- Lets in only certificates Grantra signed for that target, from this
  tenant's people CA, still valid, whose person is active and holds the
  access right now. A certificate for another target, from another CA or
  of a target reached directly is refused. It checks again for every
  session opened on a connection (a kept-open `ControlMaster` one too), and
  the connection ends when the certificate does.
- Logs in to the target with a fresh key and a certificate of the gateway
  CA that lasts two minutes, for that one session.
- Learns the target's **host key** on the first connection and refuses the
  server from then on if it changes. When it changes on purpose (a rebuilt
  server), an admin clicks **Forget host key** in the target's settings.
  Changing a target's address forgets it too.
- Checks access again every 30 seconds: when the access ends (an activation
  runs out, a review revokes it, the person leaves) or an admin clicks
  **End**, the person sees why and the session closes, with the whole
  connection. If it cannot check three times in a row (the database is
  down), the session ends too.
- Allows shells and commands only: no port forwarding, agent forwarding,
  X11 or SFTP, since what they carry would not be recorded.

## Recordings and commands

Every session through the gateway is recorded in
[asciicast v2](https://docs.asciinema.org/manual/asciicast/v2/) and stored
in the database as it happens (a crash loses seconds of it). Admins and
auditors see them in **Servers → Sessions** and play them back; watching a
recording is in the audit log. A session whose recording reaches 100 MB is
ended, so nothing runs unrecorded.

The commands typed are indexed for **Servers → Commands**: search
`DROP TABLE` and see who typed it, where and when, and jump to that moment
in the recording. Indexing is best effort, from the keys typed: in a
terminal a line only counts once the server echoed it, so what is typed at
a password prompt is not indexed, and lines recalled from shell history or
completed with Tab may be partial. For a command run without a terminal
(`ssh prod-db@gateway 'sudo -S true' < file`) only the command is indexed,
not what goes in to it; a shell without a terminal (`ssh -T`) has every
line it reads indexed, since nothing echoes. The recording is the record.

A session whose gateway stops reporting (it crashed) is marked *lost*
within a few minutes by the job **end lost SSH sessions**.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/ssh` | Everyone: whether certificates can be signed, the gateway address and both CA public keys (`ca_public_key`, `gateway_ca_public_key`) |
| `GET /api/v1/ssh/ca.pub`, `GET /api/v1/ssh/gateway_ca.pub` | Anyone, without signing in: the people and gateway CA public keys, for servers |
| `GET /api/v1/ssh/targets` | Admins and auditors see all; everyone else the targets they hold |
| `POST /api/v1/ssh/targets`, `PUT`/`DELETE /api/v1/ssh/targets/{id}` | `vault.manage` (admins). `PUT` takes `address`, `notes`, `max_certificate_hours`, `via_gateway`, `forget_host_key` |
| `POST /api/v1/ssh/targets/{id}/certificates` | Only people holding the target's access, admins included: `{"public_key"}` |
| `GET /api/v1/ssh/sessions?target=&identity=&active=true&limit=` | `vault.read` (admins, auditors) |
| `GET /api/v1/ssh/sessions/{id}` | `vault.read`: the session and its commands |
| `GET /api/v1/ssh/sessions/{id}/recording` | `vault.read`: the asciicast file |
| `POST /api/v1/ssh/sessions/{id}/terminate` | `vault.manage` |
| `GET /api/v1/ssh/commands?q=&target=&limit=` | `vault.read` |

Audit events: `ssh.target_created`, `ssh.target_updated`,
`ssh.target_deleted`, `ssh.certificate_issued` (with the key fingerprint and
serial), `ssh.session_started`, `ssh.session_ended`,
`ssh.session_terminate_requested`, `ssh.host_key_learned`,
`ssh.recording_viewed`.
