# CSV files

Grantra reads two kinds of CSV file:

- **HR exports** feed an [HR source](../identity-lifecycle.md): one row per person, and every sync turns the differences into joiners, movers and leavers.
- **Account files** feed an application whose connector is `csv` (see [connectors](../connectors.md#csv-files)): the application's user list, for systems Grantra cannot reach with SCIM or LDAP.

Both are uploaded from the console (**Settings → HR sources**, or an application's page) or sent by a script with a [service token](../service-tokens.md). Both are checked first: **Check file** is a dry run that shows what would change and every problem, and nothing changes until you apply or import it.

Templates to start from: [`hr-people.csv`](../../web/public/templates/hr-people.csv) and [`app-accounts.csv`](../../web/public/templates/app-accounts.csv). The console offers the same files from the **?** next to each upload, at `/templates/hr-people.csv` and `/templates/app-accounts.csv`.

## The file itself

These rules are the same for both kinds.

| Rule | Detail |
|---|---|
| Separator | A comma. Spreadsheets set to Spanish, French or German often save with `;`, which shows up as a missing column (see [when a file is refused](#when-a-file-is-refused)) |
| Encoding | UTF-8. A byte order mark at the start (Excel's "CSV UTF-8") is fine |
| Header | The first row names the columns. Names are matched loosely: case, spaces and punctuation do not count, so `Employee ID`, `employee id` and `employee_id` are the same column |
| Your own column names | Map them instead of renaming them: **Edit** on the source, or **Load accounts from a file** on the application, has a box per field. A sample file shows which fields it already has |
| Other columns | Kept as attributes of the person or the account, under their name in snake_case (`Cost Center` becomes `cost_center`) |
| Rows | Every row has as many cells as the header. Blank rows are skipped. Values are trimmed |
| Quotes | A value with a comma, a quote or a line break goes between double quotes, and a quote inside it is doubled: `"García, Ana"`, `"the ""new"" team"` |
| Size | 32 MiB per file |
| Line numbers | Problems name the line in the file: the header is line 1, so the first person or account is line 2 |

## HR exports

One row per person.

| Column | Required | Format | Meaning |
|---|---|---|---|
| `employee_id` | yes | text | The person's stable id in HR. Unique in the file; never reuse one |
| `display_name` | yes, or `first_name` and `last_name` | text | The full name. When empty, Grantra joins `first_name` and `last_name` |
| `first_name`, `last_name` | | text | |
| `email` | | an address with `@` | Work email. Also links people who already exist in Grantra on the first sync |
| `manager_id` | | an `employee_id` | The manager, who may be anywhere in the file. Approvals and reviews go to them |
| `department`, `title`, `location` | | text | Job attributes. A change in any of them, or of manager, makes the person a **mover** |
| `status` | | see below | Empty means active |
| `start_date`, `end_date` | | `YYYY-MM-DD`, such as `2026-10-31` | A future start keeps the person pending until that day; the day after `end_date` they are a leaver |

`status`, in any case:

| Means | Values |
|---|---|
| Works here | empty, `active`, `a`, `employed` |
| Has left | `terminated`, `inactive`, `left`, `leaver`, `t` |

Source settings that change how a file is read:

- **People missing from a file**: *become leavers* (the default) for full exports; *stay as they are* for files that only carry changes.
- **Leaver limit**: a file that would turn more than this share of the source's active people into leavers (default 10%) is stopped, unless you apply it anyway. Up to three leavers always pass.

## Account files

One row per account, or one row per account and entitlement: rows of the same account are merged, so the result of a "users join roles" query works as it is.

| Column | Required | Format | Meaning |
|---|---|---|---|
| `account_id` | yes, or `username` | text | The account's stable id in the application. Without this column, `username` is the id |
| `username` | | text | The login name |
| `email` | | text | |
| `display_name` | | text | |
| `employee_id` | | an HR `employee_id` | The person who uses the account: the surest way to link it to them. Without it, Grantra links by email, or by a user name that is an email address |
| `status` | | see below | Empty means active |
| `entitlements` | | names separated by `;` | The roles or groups the account holds. Entitlements are created from the names found. The application's settings can change the separator |
| `last_login` | | `2026-10-19` or `2026-10-19T08:30:00Z` | When the person last signed in with the account. Grantra uses it to find access nobody uses (Governance → Insights) |

`status`, in any case:

| Means | Values |
|---|---|
| Can sign in | empty, `active`, `enabled`, `a`, `yes`, `y`, `true`, `1` |
| Cannot | `disabled`, `inactive`, `locked`, `suspended`, `d`, `no`, `n`, `false`, `0` |

Accounts that were in the last file and are missing from the new one are marked as gone. A file with no accounts at all is refused when the application had some, since that is almost always a broken export.

## When a file is refused

A file with any error changes nothing: Grantra lists every problem with its line (the console shows the first 20), and the run is kept in the history as invalid or failed. Fix the file, or the column mapping, and check it again. From the API the answer is `422`, or `409` when the leaver limit stops an HR export, and `413` when the file is too large.

| What Grantra says | Why | Fix |
|---|---|---|
| `no "employee_id" column (map it with columns.employee_id)` | The header has no column Grantra recognises as the employee id | Map your column (`Worker ID`, `Personnel number`) to the field, or rename it |
| `no "account_id" column, nor "username" (map them with columns)` | The same, in an account file | Map the column that identifies each account |
| `no "display_name" column, nor "first_name" and "last_name"` | An HR export needs a name | Add or map a full name column, or first and last name |
| Any of the three above, on a file that has the column | The file is separated with `;`, so Grantra reads a single column called `employee_id;first_name;…` | Save it again as "CSV UTF-8 (comma delimited)", or export with a comma |
| `column "Email" appears twice` | Two headers are the same once matched loosely (`Email` and `EMAIL`) | Remove or rename one |
| `line 7: wrong number of fields` | A row has more or fewer cells than the header, usually a value with a comma and no quotes | Quote that value. Nothing after a broken line can be trusted, so this one stops the read |
| `line 7: bare " in non-quoted-field` | A double quote inside a value that is not quoted | Quote the whole value and double the quote inside it |
| `employee_id "1001" already appears on line 3` | Two people with the same id | Keep one row per person |
| `account "u-001": email is "a@x" here but "b@x" on line 3` | Two rows of one account disagree | Make the rows of an account agree; only entitlements may differ |
| `employee_id is empty` / `account_id is empty` | A row without its id | Fill it in or remove the row |
| `the person has no name` | Name columns empty on that row | Fill in the name |
| `email "ana" is not an email address` | No `@` in the email | Fix or empty it |
| `unknown status "on leave" (use active or terminated)` | A status Grantra does not know (see the tables above) | Use one of the listed values. Someone on leave still works here: `active` |
| `start_date "31/10/2026" is not a date like 2026-10-31` | Dates must be `YYYY-MM-DD` | Format the column as `yyyy-mm-dd` before saving |
| `end date is before start date` | | Fix one of the dates |
| `employee id 1001 belongs to another source` | Another HR source already owns that person | Sync each person from one source only |
| `12 of 80 active people would become leavers, more than the 10% limit` | The leaver limit | Check the file is complete. If it is right, **Apply anyway** |
| `the application returned no accounts, but 40 were there last time` | An account file with only a header | Check the export |
| `the file is empty` | No header row | Export again |
| `the file is larger than 32 MiB` | | Split it, or send only the columns Grantra needs |

Warnings do not stop a file. `manager 1009 is not in the source; manager left empty` means the manager's row is missing; the person is still synced, with no manager, until a file has both.
