# Five-minute demo

The dev stack ships a small sample company. This walkthrough takes you from a
new hire to a leaver in the console, signing in as the people involved.

## Start

Requires Docker.

```sh
make up
```

Open http://localhost:8080 and sign in. Every demo person signs in to
Grantra Login with their first name, without accents, and the password
`demo`. The admin is `admin` / `admin`. To switch people, sign out first, or
use a private window for each.

Emails Grantra sends land in Mailpit: http://localhost:8025.

To start over with a fresh company, run `make reset`, then `make up`.

## The sample company

`GRANTRA_DEMO=true` (set in `deploy/docker-compose.yml`) loads it on first
start, when the tenant has no HR source yet:

| Person | Login | Department | Title | Manager |
|---|---|---|---|---|
| Ana García | `ana` | Executive | CEO | |
| Luis Pérez | `luis` | Engineering | CTO, owns GitHub | Ana |
| Marta Ruiz | `marta` | Engineering | Backend Engineer | Luis |
| Diego Navarro | `diego` | Engineering | Frontend Engineer | Luis |
| Pedro López | `pedro` | Finance | Head of Finance, owns the ERP | Ana |
| Sara Martín | `sara` | Finance | Accounts Payable | Pedro |
| Lucía Torres | `lucia` | Sales | Head of Sales, owns the CRM | Ana |
| Carlos Vega | `carlos` | Sales | Account Executive | Lucía |

On top of the people:
- **Applications**: GitHub, ERP, CRM and Slack, each with a few entitlements.
- **Roles**: Engineer, Finance clerk and Sales rep. Each is given to its department by a birthright rule.
- **Manual access**: Pedro was granted *Approve payments* by hand.
- **Separation of duties**: the policy *Create and approve payments* (prevent mode) says nobody may hold both.
- **Just-in-time access**: nobody holds GitHub *Org admins* for good; Luis is eligible to activate it for up to two hours.
- **Vault**: the root password of a production database, which only people granted *Use Production database root* can check out (needs `GRANTRA_VAULT_KEY`, set in docker-compose). Luis can also break the glass on it in an emergency.
- **Remote access**: the shop's orders database, *shop-db*, which Luis can activate and query through the gateway, logged as he goes, and the office desktop *office-pc*, which he can open in the browser, recorded (both need the vault key too).

`elena` signs in once she joins in step 1.

GitHub, ERP, CRM and Slack have no connector, so nothing is provisioned there:
Grantra keeps track of who should have what. **Grantra Login** is connected
for real: a few minutes after start (or right away with **Read accounts now** on its
page under **Administration** → **Applications**), Grantra reads its users and
the groups *VPN users* and *Wiki editors*, and links each account to its
person. Turn provisioning on for it to see joiners get a Grantra Login
account with a "set up your account" email in Mailpit, and leavers get
disabled. See [connectors.md](connectors.md) to connect a real application.

## 1. Joiner

Sign in as `admin` and go to **Administration** → **HR sources**. For
*HR (demo)*, choose the file `deploy/samples/demo/2-joiner.csv` and click
**Check file**. Nothing changes yet: Grantra shows that Elena Ortiz joins.

Click **Apply**. Elena now exists. Sign in as `elena` and open **My access**:
she already has the Engineer role, given by the rule *Engineering gets
Engineer*.

## 2. Access request

Sign in as `marta`. In **Request access**, search for *Org admins* (GitHub,
high risk) and click **Request**. Say why you need it and click **Send
request**.

Luis, her manager, gets an email in Mailpit. Luis also owns GitHub, so there
is a single approval.

## 3. Approval

Sign in as `luis` and open **Approvals**. Read Marta's reason and click
**Approve**. The access is granted right away, and Marta gets an email.

## 4. Separation of duties

Sign in as `sara`. She enters payments as a Finance clerk. In **Request
access**, search for *Approve payments* and click **Request**. Grantra warns
before anything is sent: together with what she has, it breaks the *Create and
approve payments* policy, so an admin will have to accept the risk. Say why
she needs it (say, covering Pedro's leave) and click **Send request**.

Sign in as `pedro`, her manager and the owner of the ERP, and **Approve** it in
**Approvals**. The request now waits for an admin. As `admin`, **Approvals**
explains the conflict: write why the risk is acceptable, pick for how long, and
click **Accept the risk and approve**. Sara gets the access, and the violation
is recorded as an exception that reopens when its time is up.

Back as `admin`, open **Administration** → **Separation of duties**. Sara is
listed as an exception, with your reason. Pedro shows up as an open violation: he enters payments as a Finance clerk and was
also granted *Approve payments* by hand. You can **Accept the risk** for a
time, with a reason, or remove one side of it. The admins also got an email
about it.

## 5. Access review

As `admin`, open **Access reviews** → **Campaigns** and click **New
campaign**. Name it, keep *Who reviews* on the manager, and click **Launch
campaign**. Every reviewer gets an email with how many items they have.

Sign in as `luis` and open **Access reviews**. Marta's *Org admins* is
waiting. Click **Revoke**: the access is removed right away.

## 6. Leaver

As `admin`, back in **HR sources**, check and apply
`deploy/samples/demo/3-leaver.csv`. Marta leaves. Open **People** →
**Marta Ruiz**:
- She shows as *Left*.
- Her access is gone.
- Her joiner, mover and leaver events and her history tell the whole story.

She can no longer sign in to Grantra.

## 7. Privileged access

The demo vault holds the root password of a production database. Nobody can
read it yet, not even the admin.

1. Sign in as `luis`. In **Request access**, search for *Use Production
   database root*. Vault access is just in time only: pick *The access
   itself, once, for a set time* and *1 day* (or ask to be eligible, and
   activate it from **My access** when needed).
2. As `ana` (his manager), then as `admin` (the vault has no owner, so admins
   decide), approve it in **Approvals**.
3. Back as `luis`, open **Vault**, click **Check out**, give a reason and see
   the password. It is his for 30 minutes, and nobody else can take it out
   meanwhile. Click **Check in** when done.
4. Still as `luis`, open **My access**: he is eligible for GitHub *Org
   admins*. Click **Activate**, pick up to 2 hours and say why. He has it at
   once, and it goes away on its own when the hours run out (or click
   **Done, end it now**).
5. Emergency: as `luis`, click **Break glass** on the database account and
   describe the incident. Every admin and auditor gets an email (see
   Mailpit), and the password changes once he checks in if the account can
   rotate.
6. As `admin`, open **Vault** → **Break glass** and mark Luis's use
   *Justified*, or flag it with a reason.

7. SSH: still as `luis`, activate *Log in to prod-db as postgres* from
   **My access**, open **Servers**, click **Get certificate** and paste a
   public key. He gets a certificate that ends with the activation and the
   command to connect through the gateway on `localhost:2222`. The demo
   server does not exist, so to see a recording add a target of your own
   that trusts Grantra (see [ssh.md](ssh.md#set-up-a-server)).
8. Database: activate *Connect to the shop-db database* from **My access**,
   open **Remote access**, click **Get token** and **Download
   certificate**, and connect with the command shown, giving the token as
   the password:

   ```sh
   psql "host=localhost port=15432 user=shop-db sslmode=verify-full sslrootcert=grantra-gateway-ca.pem"
   ```

   Run `SELECT * FROM orders;`. As `admin`, **Remote access** →
   **Sessions** shows the session and every query in it. The shop database
   is created when the Postgres volume is first made: after upgrading, run
   `make reset`, then `make up`.
9. Desktop: start the stack with the test desktop,
   `docker compose -f deploy/docker-compose.yml --profile rdp up`, activate
   *Use the office-pc desktop* and click **Connect** in **Remote access**.
   A Linux desktop opens in a new tab, logged in. As `admin`, **Remote
   access** → **Sessions** → **Recording** plays it back.

As `admin`, click the account's name in **Vault** to see every check-out and
its reason, and **Administration** → **Standing access** to see who holds
high-risk access for good and turn it into eligibility. Once Grantra Login has been read, the **Discovery** tab lists its
accounts that look privileged or belong to nobody, and **Servers** shows the
key servers trust and every recorded session. See [vault.md](vault.md),
[just-in-time.md](just-in-time.md), [ssh.md](ssh.md) and
[gateways.md](gateways.md).

## Everything is on record

Open **Audit log**. Every step above is there: who did it, to whom, and when.
That includes the sync, the request, the approval, the accepted risk, the
review decision, the leaver and the vault check-out.
