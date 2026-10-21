# Connectors

A connector is how Grantra talks to an application. An *aggregation* **reads** the application's entitlements (groups, roles) and accounts, with who holds what, and stores them as the actual state next to the assignments (the desired state, see [access.md](access.md)). *Provisioning* **writes**: it changes the application so the actual state matches the desired one.

In the console, **Settings → Connectors** connects an application in three steps (which application, its settings, a first read to test them) for Microsoft Entra ID, Google Workspace, Okta, GitHub, AWS IAM Identity Center, Salesforce, SCIM, LDAP / Active Directory, Grantra Login and CSV files. Workday and BambooHR are in the gallery too, but they connect as [HR sources](identity-lifecycle.md#hr-systems-grantra-reads-itself): they say who works here rather than who has access. **Change settings** on the application's page changes a connector later (a new client secret when the old one expires, say). Both save the same config as the API calls below, which also cover the optional settings.

## Secrets

A connector needs a credential: an API token, a client secret, a service account key. There are two ways to give it:

- **Type it in the console.** Grantra seals it with the [vault key](vault.md) (AES-256-GCM under its own data key, which the vault key or KMS wraps), stores it in the database and puts a reference such as `secret:7c9d…` in the connector config. Nobody can read it back, from the console or the API; it can only be replaced. This needs a vault key: without one, the console says so and asks for an environment reference instead. `grantra vault rewrap` re-wraps these secrets too when the vault key changes. From the API: `POST /api/v1/connector-secrets` with `{"label": "Okta API token", "value": "..."}` answers `{"ref": "secret:..."}`.
- **Keep it in the server's environment** and give a reference, `env:NAME`, where `NAME` must start with `GRANTRA_SECRET_`. The prefix keeps a connector config from naming the server's own settings (such as `GRANTRA_DATABASE_URL`) and sending them to an outside URL.

Either way the config holds only a reference, so it is safe to show and to write in the audit log.

## Microsoft Entra ID

The `entra-id` connector reads a Microsoft Entra ID (Azure AD) tenant through Microsoft Graph. Groups (security and Microsoft 365) and the **directory roles** someone holds (Global Administrator, Privileged Role Administrator...) become entitlements; users become accounts, guests included.

1. In the Entra admin center, **App registrations → New registration** (single tenant, no redirect URI).
2. **API permissions → Add → Microsoft Graph → Application permissions**: `User.Read.All`, `GroupMember.Read.All`, `RoleManagement.Read.Directory` and, for last sign-ins, `AuditLog.Read.All`. **Grant admin consent**.
3. **Certificates & secrets → New client secret**. Note when it expires: change it before then from the application's page.

```sh
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "entra-id",
  "config": {"tenant_id": "contoso.onmicrosoft.com", "client_id": "<application id>", "client_secret": "secret:..."}
}'
```

| Config | Meaning |
|---|---|
| `tenant_id` | The directory (tenant) id, or a verified domain |
| `client_id` | The app registration's application (client) id |
| `client_secret` | The client secret (see [Secrets](#secrets)) |
| `graph_url`, `login_url` | For national clouds, e.g. `https://graph.microsoft.us` and `https://login.microsoftonline.us`. Default the global cloud |

Accounts bring the user principal name (username), `mail` (or the UPN), `employeeId` for correlation, `accountEnabled`, `jobTitle`, `department` and `userType` (`Member` or `Guest`). **Last sign-in** (`signInActivity`, for [unused access](access-insights.md)) needs an Entra ID P1 licence and `AuditLog.Read.All`; without them the read goes on without it. Group members that are not users (devices, nested groups, service principals) are left out: a person holds the groups they were put in directly. Directory roles show up with "(directory role)" after their name, so give them the risk they deserve in the catalog.

To provision, add `GroupMember.ReadWrite.All` (groups), `RoleManagement.ReadWrite.Directory` (directory roles) and `User.EnableDisableAccount.All` with `User.Read.All` (enable and disable). Grantra adds and removes members and blocks or unblocks sign-in; it does not create users, which come from HR through your usual onboarding. Dynamic groups and groups synced from on-premises AD cannot be changed through Graph: the action fails with Graph's reason.

## Google Workspace

The `google-workspace` connector reads a Google Workspace through the Admin SDK Directory API. Groups and **admin roles** (Super Admin and the others) become entitlements; users become accounts.

1. In Google Cloud, create a project and a **service account**, enable the **Admin SDK API**, and create a JSON key for the service account.
2. In the Google Admin console, **Security → Access and data control → API controls → Domain-wide delegation → Add new**: the service account's client id and these scopes: `https://www.googleapis.com/auth/admin.directory.user.readonly`, `https://www.googleapis.com/auth/admin.directory.group.readonly`, `https://www.googleapis.com/auth/admin.directory.rolemanagement.readonly`.
3. Pick an administrator for the service account to act as (a dedicated one with a read-only admin role is best).

| Config | Meaning |
|---|---|
| `admin_email` | The administrator the service account acts as |
| `service_account_key` | The service account's JSON key (see [Secrets](#secrets)) |
| `customer` | The Workspace customer id. Default `my_customer`, the administrator's own |

Accounts bring the primary email (username and email), the `organization` external id (the employee id, for correlation), suspended or archived (disabled), the primary organization's title and department, the org unit and the **last sign-in**. Only users that are direct members of a group hold it.

To provision, also allow `https://www.googleapis.com/auth/admin.directory.user` and `https://www.googleapis.com/auth/admin.directory.group.member` for the service account. Grantra asks for them only when it changes something, so a read-only setup never needs them. It adds and removes group members and suspends or restores users; it does not create users or change admin roles.

## Okta

The `okta` connector reads an Okta organization through its management API. Groups, **application assignments** and **admin roles** become entitlements; users become accounts, deactivated ones included (as disabled).

Create an API token (**Security → API → Tokens**) signed in as an administrator with the **Read-only Administrator** role; Okta tokens act with the permissions of whoever created them.

| Config | Meaning |
|---|---|
| `org_url` | The Okta organization, e.g. `https://acme.okta.com` |
| `token` | The API token (see [Secrets](#secrets)) |
| `apps` | Default `true`: also read who is assigned to each active application, as entitlements named "Salesforce (application)". Each application costs one more call per read; `false` leaves them out |

Accounts bring the login (username), email, `employeeNumber` for correlation, title, department, user type, Okta status and the **last sign-in**. Users who are active, staged, provisioned, locked out or in password recovery count as active; suspended and deactivated ones as disabled.

To provision, the token's administrator needs to manage the groups Grantra changes (Group Administrator for them, or more). Grantra adds and removes group members and suspends or unsuspends users; deactivated users are reactivated in Okta, by a person. It does not change application assignments or admin roles directly: assign applications to groups in Okta and govern the groups.

## GitHub

The `github` connector reads a GitHub organization, on github.com or GitHub Enterprise Server. Teams and the **organization owner** role become entitlements; members and **outside collaborators** become accounts (the attribute `membership` says which).

Create a token as an organization owner: a classic token with `read:org`, or a fine-grained token for the organization with read access to **Members**. With `admin:org` (classic), Grantra also reads the organization's **SAML identities**, and each account gets the email its person signs in with, so it links to them; without SAML, GitHub accounts have no work email and are linked by hand once (the link stays).

| Config | Meaning |
|---|---|
| `organization` | The organization's login, e.g. `acme` |
| `token` | The token (see [Secrets](#secrets)) |
| `api_url` | For GitHub Enterprise Server, e.g. `https://github.example.com/api/v3`. Default `https://api.github.com` |

Accounts are named by the GitHub user id, which survives a change of login. A team's members include the members of its child teams, as on GitHub. GitHub does not say when someone last signed in.

To provision, the token needs `admin:org` (classic) or write access to **Members** (fine-grained). Grantra adds and removes team members, makes members owners or plain members, and **removes leavers from the organization** (members and outside collaborators): GitHub accounts belong to their people, so that is how their access ends. It does not invite people.

## AWS IAM Identity Center

The `aws-identity-center` connector reads who can reach which AWS account, and how, from AWS IAM Identity Center (formerly AWS SSO). Groups and **account assignments** (a permission set on an account, such as "AdministratorAccess on 111111111111") become entitlements; Identity Center users become accounts. An assignment to a group counts for each of its members: they can all reach the account.

Give Grantra an IAM identity in the account that holds Identity Center (the management account, or the delegated administrator) with this policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "sso:ListInstances", "sso:ListPermissionSets", "sso:DescribePermissionSet",
      "sso:ListAccountsForProvisionedPermissionSet", "sso:ListAccountAssignments",
      "identitystore:ListUsers", "identitystore:ListGroups", "identitystore:ListGroupMemberships"
    ],
    "Resource": "*"
  }]
}
```

| Config | Meaning |
|---|---|
| `region` | Where the Identity Center instance lives, e.g. `eu-west-1` |
| `instance_arn` | Only when the account sees more than one instance |
| `access_key_id`, `secret_access_key` | An IAM user's keys (the secret as in [Secrets](#secrets)). Leave both out to use the server's own AWS identity: environment, IAM roles for service accounts on EKS, or the instance's role |
| `role_arn` | Optional role to assume first, e.g. a read-only role in the management account |

Accounts bring the username, display name, primary email, title and whether the user is enabled. AWS accounts are shown by their id. The connector **only reads**: people and groups usually come into Identity Center from your identity provider (Entra ID, Okta, Google), where Grantra changes them; provisioning cannot be turned on for it.

## Salesforce

The `salesforce` connector reads a Salesforce org through its REST API. **Profiles**, **permission sets** and **permission set groups** become entitlements; users become accounts, with their last sign-in.

1. In Setup, create an **External Client App** (or a connected app) with OAuth on, the `api` scope and the **client credentials flow** enabled, and pick the user it runs as: an integration user who can **View Setup and Configuration** and **View All Users** (to provision, **Manage Users** and **Assign Permission Sets**).
2. Copy its consumer key and secret.

| Config | Meaning |
|---|---|
| `instance_url` | The org's My Domain address, e.g. `https://acme.my.salesforce.com` |
| `client_id` | The consumer key |
| `client_secret` | The consumer secret (see [Secrets](#secrets)) |
| `api_version` | Default `v62.0` |

Accounts bring the username, email, name, `EmployeeNumber` for correlation, the federation id (often the corporate email), title, department, user type, whether the user is active and the **last sign-in**. Every user holds their profile. The permission sets that profiles and permission set groups keep behind the scenes are left out.

To provision, Grantra assigns and removes permission sets and permission set groups, gives users another profile (a profile cannot be taken away, only replaced), and activates or deactivates users; Salesforce never deletes users. It does not create users.

## Rate limits

Every connector that calls a web API waits and tries again, up to five times, when the service says it is busy (`429 Too Many Requests`, or GitHub's `403` with its limit used up), as long as the service asks (`Retry-After`) or with a growing pause. Reads also try again on `502`, `503` and `504`. A wait longer than a minute fails the read instead, which changes nothing; the next hourly read tries again.

## SCIM 2.0

This connector speaks [SCIM 2.0](https://scim.cloud/), which Okta, Entra ID, Slack, GitHub Enterprise, Zoom and most SaaS tools offer.

```sh
export GRANTRA_SECRET_CRM_SCIM_TOKEN=...   # in the server's environment
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "scim",
  "config": {"base_url": "https://crm.example.com/scim/v2", "token": "env:GRANTRA_SECRET_CRM_SCIM_TOKEN"}
}'
curl -b cookies -X POST localhost:8080/api/v1/applications/$APP/aggregations
```

| Config | Meaning |
|---|---|
| `base_url` | The SCIM endpoint. Must be https; `allow_http: true` is for labs only |
| `token` | The bearer token: typed in the console, or a reference to the server's environment (see [Secrets](#secrets)) |
| `page_size` | Resources per request, default 100 |

Groups become entitlements. Membership comes from each group's `members` and each user's `groups`. Users bring `userName`, primary email, `externalId` (or the enterprise `employeeNumber`), `active`, `title` and `department`.

The config is checked when it is saved (URL, token reference that resolves), so mistakes show up before the first aggregation. Only admins configure connectors; they point the server at URLs of their choosing, so treat that permission accordingly.

## LDAP and Active Directory

The `ldap` connector reads an LDAP directory: Active Directory, or OpenLDAP. Point an application (say, *Corporate directory*) at it to govern who is in which directory group.

```sh
export GRANTRA_SECRET_AD_PASSWORD=...   # in the server's environment
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "ldap",
  "config": {
    "url": "ldaps://dc1.corp.example.com",
    "bind_dn": "CN=grantra,OU=Service Accounts,DC=corp,DC=example,DC=com",
    "password": "env:GRANTRA_SECRET_AD_PASSWORD",
    "base_dn": "DC=corp,DC=example,DC=com"
  }
}'
```

| Config | Meaning |
|---|---|
| `url` | `ldaps://host[:port]`, or `ldap://host[:port]` with `start_tls: true`. Plain `ldap://` without TLS needs `allow_insecure: true` and is for labs only: the password would cross the network in the clear |
| `ca_cert` | Optional. A reference (`env:GRANTRA_SECRET_...`) to the PEM certificate of the CA that signed the server's certificate, for directories with an internal CA |
| `bind_dn`, `password` | The service account Grantra signs in with. `password` is a reference, like the SCIM token |
| `base_dn` | Where users and groups are searched. `user_base_dn` and `group_base_dn` narrow it, e.g. to `OU=People,...` and `OU=Groups,...` |
| `directory` | `active_directory` (default) or `openldap`. It sets the defaults below |
| `user_filter`, `group_filter` | Replace the default filters, e.g. `(&(objectCategory=person)(objectClass=user)(memberOf=CN=Staff,...))` |
| `attributes` | Replace default attribute names: `id`, `username`, `email`, `display_name`, `external_id`, `group_name`, `member` |
| `page_size` | Entries per page, default 500 |

| | Active Directory | OpenLDAP |
|---|---|---|
| Users | `(&(objectCategory=person)(objectClass=user))` | `(objectClass=inetOrgPerson)` |
| Groups | `(objectClass=group)` | `(\|(objectClass=groupOfNames)(objectClass=groupOfUniqueNames))` |
| Id | `objectGUID` | `entryUUID` |
| Username, email, name | `sAMAccountName`, `mail`, `displayName` | `uid`, `mail`, `cn` |
| Employee id (for correlation) | `employeeID` | `employeeNumber` |
| Disabled when | `userAccountControl` has the disable flag | `pwdAccountLockedTime` is `000001010000Z` (locked until an admin unlocks it) |
| Last sign-in (for [unused access](access-insights.md)) | `lastLogonTimestamp` (replicated, so up to two weeks behind) | `pwdLastSuccess` (needs the `ppolicy` overlay with `pwdLastSuccess` tracking) |

Groups become entitlements, named by their `cn`. Accounts hold the groups that list them as `member`; groups nested in other groups are not expanded, so a person holds what they were put in directly, which is also what Grantra grants. Members that are not users read by the user filter (computers, nested groups, people outside the filter) are left out. Accounts also carry their `dn`, `title`, `department` and `userPrincipalName` (`upn`).

Ids are the directory's own stable ids, so renaming someone or moving them to another OU keeps their account and its history. Large Active Directory groups, which the server hands out in ranges of 1,500 members, are read in full. Searches are paged; a failure anywhere fails the whole read.

The service account needs read access to users and groups. To provision, it also needs to write the `member` attribute of the groups Grantra manages and, in Active Directory, `userAccountControl` of users (in OpenLDAP, `pwdAccountLockedTime`, with the `ppolicy` overlay loaded so locked accounts cannot sign in).

## Grantra Login

The `grantra-login` connector governs [Grantra Login](grantra-login.md), the Keycloak that ships with Grantra, through the Keycloak Admin REST API. Realm users become accounts; groups and realm roles become entitlements.

```sh
export GRANTRA_SECRET_GRANTRA_LOGIN=...   # the grantra-connector client secret
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "grantra-login",
  "config": {"url": "https://login.example.com", "client_secret": "env:GRANTRA_SECRET_GRANTRA_LOGIN"}
}'
```

| Config | Meaning |
|---|---|
| `url` | Where Grantra reaches Keycloak. Must be https; `allow_http: true` is for docker-compose and labs |
| `realm` | Default `grantra` |
| `client_id`, `client_secret` | The confidential client Grantra signs in as (client credentials). Default client `grantra-connector`; the secret is a reference, like the SCIM token |
| `external_id_attribute` | The user attribute holding the HR employee id, for correlation. Default `employee_id` |
| `setup_email` | Default `true`: new users get Keycloak's "set up your account" email. Turn it off when people only sign in through a federated IdP |
| `roles` | Default `true`: realm roles are entitlements too, besides groups |
| `page_size` | Items per request, default 100 |

The client needs **Service accounts** on and, on its service account, the `realm-management` roles `manage-users` and `view-realm` to read and provision. The Grantra Login page in the console also needs `manage-clients` (apps), `manage-realm` (sign-in policy, domain routing and imports) and `manage-identity-providers` (federation), and [threat detection](threat-detection.md) needs `view-events` and `manage-events` to read sign-ins; leave them out if you manage those in Keycloak yourself. [`deploy/keycloak/grantra-realm.json`](../deploy/keycloak/grantra-realm.json) has it ready, with a development secret to replace.

- Groups are named by their path (`Staff/Finance`), and subgroups are read too. Keycloak 26.4 has no group description, so a `description` attribute on the group stands in for it.
- Accounts hold the groups and realm roles they were given directly; roles inherited through a group or a composite role come with that group or role. Keycloak's built-in `default-roles-<realm>`, `offline_access` and `uma_authorization` are left out.
- Accounts carry `department` and `title` when those attributes exist. For Keycloak to keep `employee_id`, `department` and `title`, the realm's user profile must declare them, as the bundled realm does (admin-only).
- Service-account users are not people and Keycloak does not list them.

## CSV files

Applications with no SCIM or LDAP (a legacy ERP, an in-house tool) can still be governed: set their connector to `csv` and load their account list from a file, from the application's page in the console (**Load accounts from a file**, then **Check file** and **Import**) or from a script with a [service token](service-tokens.md).

```sh
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "csv", "config": {"columns": {"username": "Login", "entitlements": "Roles"}}
}'
curl -b cookies -X POST "localhost:8080/api/v1/applications/$APP/account-imports?dry_run=true" \
  -H 'Content-Type: text/csv' --data-binary @deploy/samples/app-accounts.csv
```

One row per account, with a header row. Column names are matched loosely (`Account ID` is `account_id`), and `config.columns` maps other names.

| Column | Meaning |
|---|---|
| `account_id` | The account's stable id in the application. Without this column, `username` is the id |
| `username`, `email`, `display_name` | |
| `employee_id` | The person's HR employee id, the surest way to link the account to them |
| `status` | `active` (the default; also `enabled`, `yes`, `1`) or `disabled` (also `inactive`, `locked`, `suspended`, `no`, `0`) |
| `entitlements` | The roles or groups the account holds, separated by `;` (`config.separator` changes it). Entitlements are created from the names found |
| `last_login` | When the account last signed in (`2026-10-19`, `2026-10-19 08:30:00` or RFC 3339), so [unused access](access-insights.md) can tell. An import never moves it back in time |

Any other column is kept as an attribute of the account. An export with one row per account **and** role (the result of a "users join roles" query) works as it is: rows of the same account are merged, and a value that differs between them (two emails for one account) is an error. See [`deploy/samples/app-accounts.csv`](../deploy/samples/app-accounts.csv). [CSV files](help/csv-files.md) has every rule of the format, a template and what each error means.

- **All or nothing.** A file with any error is refused (`422`) with every problem and its line, and changes nothing; the run is recorded as failed.
- **Dry run.** `dry_run=true` answers with what would change, and changes nothing.
- **Full lists.** The file is the whole application: accounts missing from it are marked `deleted`, as in any aggregation. A file with no accounts, where there were some, is refused.
- The hourly aggregation leaves these applications alone, and the connector cannot provision: changes are made in the application by hand and show up with the next file.

## Aggregation

- Runs on demand (`POST /aggregations`) and on its own once an hour for every application with a connector.
- Each run is recorded in `connector_runs` with what changed (new accounts, orphans, membership changes...) and audited as `application.aggregated`.
- **A failed read changes nothing.** Neither does a read that returns no accounts where there were some last time: that looks like a broken service, not a company that deleted every user. The API answers `502` and the run is recorded as failed.
- Accounts no longer returned become `deleted`; entitlements no longer returned get `missing_since`. History is kept.

## Correlation

Each account is matched to the identity it belongs to, first match wins:

1. `externalId` equals the identity's employee id (`rule:external_id`).
2. The account email equals the identity's email (`rule:email`).
3. The username is an email that equals the identity's email (`rule:username`).

A match must be unique: an email two people share links nobody. Accounts that match nobody are **orphans**, listed with `GET /accounts?orphans=true`. An admin can link an account by hand (`PUT /api/v1/accounts/{id}/identity`), or mark it as nobody's (a shared or service login) with `{"identity_id": null}`. Manual decisions survive every aggregation.

## Provisioning

Provisioning compares what people should hold in an application (their assignments, direct or through business roles) with what the last aggregation found, and fixes the difference through the connector:

| Action | When |
|---|---|
| `create_account` | An active person should have access and has no account. The username is their email; people without an email are skipped and counted as `skipped_no_email`. If the application already has an account with that username that correlation did not link, creation fails until someone links it by hand. Connectors that cannot create accounts (LDAP) count these people as `skipped_no_account` instead: once someone makes the account, the next aggregation links it and provisioning adds what is missing |
| `enable_account` | An active person should have access and their account is disabled |
| `add_member` | An active person should hold an entitlement and none of their accounts does. It goes on their main account (the active one, else the oldest) |
| `remove_member` | Someone holds an entitlement nobody assigned them, **and** Grantra granted it itself, or the application has `remove_unassigned` on. Leavers, suspended people and people who have not started should hold nothing |
| `disable_account` | The person left or is suspended. Accounts are disabled, never deleted |

Orphan accounts are never touched: whose they are is for a person to decide (see Correlation).

### Turning it on

Provisioning is **off** for every application until an admin turns it on:

```sh
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/provisioning -d '{"mode": "on"}'
curl -b cookies -X POST "localhost:8080/api/v1/applications/$APP/provisioning-runs?dry_run=true"   # see the plan
curl -b cookies -X POST localhost:8080/api/v1/applications/$APP/provisioning-runs                  # do it
```

| Setting | Meaning |
|---|---|
| `mode` | `off` (default: Grantra only reads) or `on`. Turning it on needs a connector that can write |
| `remove_unassigned` | Default `false`: Grantra only removes memberships it granted itself, so turning provisioning on never strips access people got outside Grantra. Set it to `true` once the assignments describe everyone's access |
| `max_change_percent` | Default 10. A run that would remove or disable more than this share of the application's accounts is refused; up to three such changes always pass |

### Safety

- **Dry runs** (`?dry_run=true`) show every action and change nothing. They work while provisioning is off, to see what turning it on would do.
- **Plans start from what is really there.** A run needs a successful aggregation of the application in the last 24 hours; otherwise the API answers `409` and asks for one.
- **Big runs are refused** past `max_change_percent` (`409`, recorded as `refused` with its plan). Check the plan, then repeat with `?force=true` if it is right.
- **One run at a time per application.** A run is recorded as `running` before it changes anything. Another run of the same application answers `409`, and aggregations of it are refused until the run ends.
- **Nothing done is ever forgotten.** Each action is recorded, with the account or membership it changed, as soon as the application confirms it, not at the end of the run. If Grantra stops halfway (shutdown, lost database), the run ends as `interrupted`: actions marked `done` happened, actions still `planned` did not, and the next run plans those again. A run left `running` by a crash is closed as `interrupted` once it is older than the 10 minutes any run may take.
- **Failures don't stop the run.** Each action is tried and recorded with its outcome; the run is `succeeded`, `partial` or `failed` (`502` for the last two). The next run tries again what is still missing.
- **Everything is audited**: one `provisioning.<action>` event per action with its outcome, and `application.provisioned` per run. Runs and their actions are in `connector_runs`.
- Accounts Grantra creates are linked to their person as `provisioned`, and memberships it adds are marked `provisioned_at`. Both survive aggregations.

With provisioning on, Grantra provisions every application on its own every five minutes; runs with nothing to do are not recorded.

### SCIM

The SCIM connector writes with `POST /Users` (`userName`, `displayName`, primary work email, `externalId`), `PATCH /Users/{id}` (`active`) and `PATCH /Groups/{id}` (`add` / `remove` `members`). The token needs write access to users and groups.

### Entra ID, Google Workspace, Okta, GitHub and Salesforce

They add and remove members of groups, teams or permission sets and enable or disable users (GitHub removes leavers from the organization), with the permissions in their sections above. None of them creates accounts: people get those from HR through the directory's own onboarding, and the next aggregation links them. AWS IAM Identity Center only reads.

### LDAP

The LDAP connector adds and removes the user's DN in the group's `member` attribute, and disables or enables accounts: the disable flag of `userAccountControl` in Active Directory, `pwdAccountLockedTime` in OpenLDAP. It does not create accounts, which directories usually get from their own onboarding. An OpenLDAP `groupOfNames` must keep one member, so removing its last one fails with a message saying so; give such groups a placeholder member.

### Grantra Login

The Grantra Login connector creates users (username and verified email from HR, first and last name, `employee_id`) without a password: they get an email to set one, valid for three days, so Grantra never chooses or sees a password. The realm needs working email settings; if the email fails, the action fails and the next run sends it again (creating a user that already exists finds it instead). Disabling a user also signs them out of every session. Memberships go through `PUT`/`DELETE /users/{id}/groups/{group}` and the user's realm role mappings.

## API

| Endpoint | Action |
|---|---|
| `PUT /api/v1/applications/{id}/connector` | `catalog.manage`: `{"kind": "entra-id", "google-workspace", "okta", "github", "aws-identity-center", "salesforce", "scim", "ldap", "grantra-login" or "csv", "config": {...}}`; `"kind": "manual"` removes it |
| `POST /api/v1/connector-secrets` | `catalog.manage`: `{"label": "...", "value": "..."}`, `201` with `{"ref": "secret:..."}` to put in a config; `409` without a vault key |
| `POST /api/v1/applications/{id}/aggregations` | `catalog.manage`: `200` with the run, `502` if the application could not be read |
| `POST /api/v1/applications/{id}/account-imports?dry_run=` | `catalog.manage`, or a service token with `application.import_accounts`: the CSV file is the body (32 MiB max). `200` with the run (`planned` for a dry run), `422` with the run when the file is refused |
| `PUT /api/v1/applications/{id}/provisioning` | `catalog.manage`: `{"mode": "on", "remove_unassigned": false, "max_change_percent": 10}` |
| `POST /api/v1/applications/{id}/provisioning-runs?dry_run=&force=` | `catalog.manage`: `200` with the run and its actions, `409` when refused or not possible yet, `502` when actions failed |
| `GET /api/v1/applications/{id}/runs?limit=` | `catalog.read`: aggregation and provisioning runs |
| `GET /api/v1/applications/{id}/accounts?orphans=&after=&limit=` | `catalog.read` |
| `PUT /api/v1/accounts/{id}/identity` | `catalog.manage`: `{"identity_id": "..."}` or `null` |
| `GET /api/v1/identities/{id}/accounts` | `identity.read`: the person, their manager, helpdesk, auditors and admins |

## Writing a connector

Implement `connector.Reader` (`Read(ctx) (Snapshot, error)`) in a package under `core/internal/connector/`, register a `Factory` in `init`, and import the package in `cmd/grantra`. The factory must reject unknown config fields and resolve secrets through the `SecretResolver` it is given. `Read` must fail rather than return a partial snapshot. To provision, also implement `connector.Writer` (`SetActive`, `AddMember`, `RemoveMember`, all safe to repeat), and `connector.AccountCreator` (`CreateAccount`) if the application can create accounts. Connectors that can set passwords implement `connector.PasswordSetter` (`SetPassword`) and call `connector.RegisterPasswordSetter` in `init`, which lets the [vault](vault.md#rotation) rotate passwords through them. Connectors for web APIs share `connector/httpapi` (address checks, sign-in with a token or OAuth, waiting when the service is busy, error messages, paging by link or token), `connector.DecodeConfig` and `connector.Secret` for the config, and `connector.Builder` to put a snapshot together from lists of members; their tests fake the API with `connector/connectortest`. See `connector/okta` for a short example, and `connector/ldap` for one that is not a web API.
