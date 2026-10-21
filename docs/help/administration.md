# Administration

For Grantra admins. Applications, roles and rules, separation of duties and standing access are under **Governance** in the menu; general settings, HR sources, connectors, Grantra Login, API tokens and modules under **Settings**. Auditors see all of these pages read only.

**Home** shows admins what needs fixing: connections whose last run failed, accounts that belong to nobody, open separation of duties violations and access with no description (people ask for what they understand).

## HR sources

HR is the authority on who works here. An HR source reads an export (a CSV file) and turns the differences into joiners, movers and leavers.

1. **New HR source**: name it, choose what happens to people missing from a file, and set the leaver limit. Map your export's column names to Grantra's fields; a sample file shows which it already has.
2. Choose the export and **Check file**. Nothing changes yet: you see every joiner, mover and leaver, and every error and warning.
3. **Apply** it. A file stopped by the leaver limit can be applied anyway after you confirm.

The format of the file, a template and what each error means are in [CSV files](csv-files.md). A nightly job can send the export instead of a person, with an API token.

## Applications

Add applications and their entitlements (roles, groups). Applications connect in one of three ways:

- **Microsoft Entra ID, Google Workspace, Okta, GitHub, AWS IAM Identity Center, Salesforce, SCIM or LDAP / Active Directory**, from **Settings → Connectors**: pick the connector, pick the application (or name a new one), fill in its settings, then **Test: read accounts now**. Type the token or secret in: Grantra keeps it encrypted with the vault key and never shows it again (or give a reference such as `env:GRANTRA_SECRET_CRM_TOKEN` to a variable in the server's environment). Grantra reads the accounts every hour, and can write changes back once provisioning is on. **Change settings** on the application's page changes them later; leave a secret blank to keep the one saved.
- **Account files**, for applications with no API: **Load accounts from a file**, map the columns, then **Check file** and **Import**. See [CSV files](csv-files.md).
- **By hand**: Grantra keeps the catalog, and someone makes the changes in the application.

Each application's page shows its accounts, the ones nobody owns (orphans), past runs, and provisioning: whether Grantra writes changes back to the application, with a dry run to see what it would do first and a limit on how many changes one run can make.

## Roles and rules

Business roles bundle entitlements; birthright rules give them by job ("Engineering gets the Engineer role"). They are listed here and edited through the API.

## Approval flows

**Governance → Approval flows** says who approves what people ask for. Each policy has conditions (applications, business roles, risk, the kind of request, the department of the person, people or service accounts, for how long) and steps in order: the manager or a manager further up, the owner, named people, the holders of a business role, or the admins. A step can need one, some or all of its people, and says what happens when nobody decides in time: the request expires, is rejected, or goes to the approvers' managers.

The first policy that fits a request decides; change the order with the arrows. When none fits, the manager decides, then the owner. **Try it** shows who would approve a request before you save. Some rules hold whatever a policy says: nobody approves their own access, only low risk access can go with no approval, and a separation of duties risk always needs an admin.

## Separation of duties

Policies say which access must not be held together. Grantra checks every 30 minutes, or on request. In *prevent* mode, a request that would break a policy needs an admin to accept the risk. A violation's risk can be accepted for a set time with a reason.

## Standing access

High and critical risk access held with no end date. Turn it into eligibility in one click, so people activate it only when they need it.

## API tokens

Tokens for scripts that send HR exports and account files. Each one is shown once, with a `curl` example, can only do what its scopes say and expires. Revoke a token you no longer use.

## General settings

**Settings → General** holds the time limits, reminders and thresholds of each part of Grantra: how long approvers have for each step, how long reviewers get, the longest separation of duties exception, the longest break-glass check-out, when access counts as unused, how long console sessions last, and more. Each one shows its default and the values it accepts; **Use the default** puts it back.

A change that makes Grantra less strict, such as a longer break-glass check-out, waits for another admin when there is one: write why, and they approve or reject it from their Inbox. You can withdraw it while it waits. Stricter changes take effect at once. Every change is in the audit log.

## Modules

**Settings → Modules** turns privileged access (vault, servers, remote access) or Grantra Login off for the whole company, for those who don't use them. Their pages, search results and inbox tasks disappear for everyone. It doesn't change anyone's access or stop the gateways: remove the access, or turn the gateway off, for that.
