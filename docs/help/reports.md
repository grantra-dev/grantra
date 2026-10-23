# Reports for auditors

**Governance → Reports** gives auditors the evidence they ask for, from what Grantra already records. Admins and auditors see it.

## Run a report

Pick a report, then the dates it covers (the last full quarter by default) and, for some, one application. You see a few counts at the top and the first 100 rows of each list. Then:

- **CSV** next to each list downloads all of it, ready for Excel.
- **Printable summary, or PDF** opens a page to print or save as PDF.
- **Evidence pack (.zip)** has every list as CSV, the summary, and a `manifest.json` with a fingerprint (SHA-256) of each file.

| Report | What it answers |
|---|---|
| Access reviews | Who reviewed each access, what they decided, and whether revoked access is really gone |
| Leavers and orphan accounts | How long each leaver's accounts took to close against your target, and accounts nobody owns |
| Access requests and approvals | Who asked for what, why, and who approved each step |
| Separation of duties | The policies, every violation, and who accepted each exception and why |
| Privileged access | Just-in-time access, vault and break-glass use with their reviews, recorded sessions |
| Who has access to what | Everything everyone holds now, and whether Grantra gave it |
| Administration changes | Who the admins are, and every change to how Grantra is set up |
| Identity threats and non-human identities | Alerts and how they were handled, and service accounts with no owner |

Every download is in the audit log with its fingerprint.

## Frameworks

**Frameworks** maps the controls of SOX, ISO 27001, SOC 2 and NIS2 to the reports that cover them. **Evidence pack** on a framework downloads everything its controls need for the dates you picked, in one file.

Admins can rename the controls to their own ids, change which reports cover each one, turn a framework off, or add their own (DORA, ENS, an internal policy). **Back to defaults** undoes the changes to a built-in one.

## Verify a file

**Verify a file** tells whether a pack or report someone hands you came out of Grantra unchanged: every file of a pack must match its manifest, and Grantra must have recorded handing it out.

## Settings

Admins set under **Settings → General → Compliance reports** how fast a leaver's accounts must close (24 hours by default), how long the usual period is, and the first month of the fiscal year.
