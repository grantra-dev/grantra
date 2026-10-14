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

`elena` signs in once she joins in step 1.

The applications have no connector, so nothing is provisioned. Grantra keeps
track of who should have what. See [connectors.md](connectors.md) to connect
a real application.

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

## Everything is on record

Open **Audit log**. Every step above is there: who did it, to whom, and when.
That includes the sync, the request, the approval, the accepted risk, the
review decision and the leaver.
