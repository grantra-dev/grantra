# Administration

For Grantra admins. Applications, roles and rules, separation of duties and standing access are under **Governance** in the menu; HR sources, connectors, Grantra Login, API tokens and modules under **Settings**. Auditors see all of these pages read only.

**Home** shows admins what needs fixing: connections whose last run failed, accounts that belong to nobody, open separation of duties violations and access with no description (people ask for what they understand).

## HR sources

HR is the authority on who works here. An HR source reads an export (a CSV file) and turns the differences into joiners, movers and leavers.

1. **New HR source**: name it, choose what happens to people missing from a file, and set the leaver limit. Map your export's column names to Grantra's fields; a sample file shows which it already has.
2. Choose the export and **Check file**. Nothing changes yet: you see every joiner, mover and leaver, and every error and warning.
3. **Apply** it. A file stopped by the leaver limit can be applied anyway after you confirm.

The format of the file, a template and what each error means are in [CSV files](csv-files.md). A nightly job can send the export instead of a person, with an API token.

## Applications

Add applications and their entitlements (roles, groups). Applications connect in one of three ways:

- **SCIM or LDAP / Active Directory**, from **Settings → Connectors**: pick the connector, pick the application (or name a new one), fill in its settings, then **Test: read accounts now**. Secrets are never typed in: you give a reference such as `env:GRANTRA_SECRET_CRM_TOKEN`, and the secret lives in the server's environment. Grantra reads the accounts, and can write changes back once provisioning is on. Changing an existing connector's settings is done through the API.
- **Account files**, for applications with no API: **Load accounts from a file**, map the columns, then **Check file** and **Import**. See [CSV files](csv-files.md).
- **By hand**: Grantra keeps the catalog, and someone makes the changes in the application.

Each application's page shows its accounts, the ones nobody owns (orphans), past runs, and provisioning: whether Grantra writes changes back to the application, with a dry run to see what it would do first and a limit on how many changes one run can make.

## Roles and rules

Business roles bundle entitlements; birthright rules give them by job ("Engineering gets the Engineer role"). They are listed here and edited through the API.

## Separation of duties

Policies say which access must not be held together. Grantra checks every 30 minutes, or on request. In *prevent* mode, a request that would break a policy needs an admin to accept the risk. A violation's risk can be accepted for a set time with a reason.

## Standing access

High and critical risk access held with no end date. Turn it into eligibility in one click, so people activate it only when they need it.

## API tokens

Tokens for scripts that send HR exports and account files. Each one is shown once, with a `curl` example, can only do what its scopes say and expires. Revoke a token you no longer use.

## Modules

**Settings → Modules** turns privileged access (vault, servers, remote access) or Grantra Login off for the whole company, for those who don't use them. Their pages, search results and inbox tasks disappear for everyone. It doesn't change anyone's access or stop the gateways: remove the access, or turn the gateway off, for that.
