# Attack paths and access insights

Grantra knows every way someone gets access: an assignment, a role, a birthright rule, an eligibility, an account in the application, a shared account in the vault. Put together, that answers two questions an access list alone does not:

- **Attack paths**: who can reach the riskiest access, and how. Whoever takes over one of those people gets the same.
- **Insights**: which access nobody uses, what a reviewer should look at twice, and which roles hide in what people hold one by one.

Both are computed when asked, from the tables Grantra already keeps; nothing runs in the background and nothing changes access on its own. Recommendations are rules and counts that say why (last use, how many peers hold it), not a model's guess. The console pages are **Security → Attack paths** and **Governance → Insights**; the in-console help is [docs/help/threats.md](help/threats.md) and [docs/help/insights.md](help/insights.md).

## Attack paths

A **target** is an entitlement with high or critical risk; Grantra's own admin role counts too. For each target Grantra walks back from the entitlement to every active person who reaches it:

| Path | Example |
|---|---|
| Given directly, by request or by hand | Ana was given Domain Admins |
| Through a role, and a birthright rule giving the role | Gina matches the rule Engineering, which gives Ops, which includes Domain Admins |
| An eligibility (just in time) | Bob may turn on Domain Admins |
| An account holding it in the application, which Grantra did not give | Carla owns the account carla-adm, which holds Domain Admins |
| A shared account the vault hands out, or the gateway logs in with | Dan requested the vault account backup, which logs in as svc-backup, which holds Domain Admins |

Shared accounts chain: if access to a vault account is itself given by a role, the path shows both, up to three shared accounts deep. Each path says whether it is **for good**, **until** a date, or **only when turned on**, and marks the ones **through a shared account** and **not given by Grantra**, the two that access lists miss.

Each target counts the people who reach it, how many for good, through a shared account and outside Grantra, and the **accounts nobody owns** that hold it (owner left or unknown, and not in the vault). A target lists at most 1,000 paths and says when it has more.

From a threat alert, the console also shows the targets the person reaches.

## Unused access

Access is **unused** when it was given by hand or by request, or is an eligibility, it was held for more than 90 days, and Grantra knows of no use in the last 90. Birthright access is left out (the rule is what to change). Grantra knows use from:

- check-outs from the vault, SSH certificates, gateway sessions and tokens;
- just-in-time activations of an eligibility;
- sign-ins to the Grantra console, for Grantra's own roles;
- the account's **last sign-in**, read from Active Directory (`lastLogonTimestamp`), OpenLDAP (`pwdLastSuccess`) or an [account file](connectors.md#csv-files) with a `last_login` column;
- [sign-ins](threat-detection.md#sign-ins) whose `application` is the application's name in Grantra.

Access that none of these cover is **not tracked** and never called unused. A role is tracked when every entitlement in it is, and its last use is the latest of any of them.

## Suggestions in access reviews

Each pending item a reviewer sees carries a suggestion, with the reasons behind it:

| Signal | When |
|---|---|
| `unused` | As above |
| `used` | Used in the last 90 days |
| `common` | At least half of the person's peers hold it (3 peers or more) |
| `rare` | At most 10% of the peers hold it (5 peers or more) |

Peers are the other active people in the person's department or, when they have none, the people with the same manager. Grantra suggests **revoke** for unused access, **keep** for used or common access, **revoke** for rare access, and nothing otherwise. The reviewer decides; the suggestion is never applied.

## Role suggestions

Grantra groups active people by department, and by department and job title, and looks for entitlements at least 80% of a group hold one by one: by hand, by request with no end date, or through an account in the application. Groups of fewer than 5 people, just-in-time only entitlements, Grantra's own roles and anything a role or rule already gives someone are left out, and a suggestion needs at least 2 entitlements. A job title's suggestion leaves out what its department's already has. At most 50 are listed, the ones covering the most grants first.

Creating one makes a business role and, optionally, a birthright rule with the group's conditions, through the same `POST /api/v1/roles` and `POST /api/v1/access-rules` as by hand. Whoever was missing part of it gets it from the rule; the grants they held one by one stay until a review or an admin removes them.

## API

Attack paths need `threat.read` (admins and auditors); insights need `catalog.read`.

| Endpoint | |
|---|---|
| `GET /api/v1/attack-paths` | `{"targets": [...]}`: each target with `people`, `standing`, `credential`, `outside` and `orphans`, riskiest and most reached first |
| `GET /api/v1/attack-paths/{entitlement}` | One target with its `paths` (each a list of `steps` from the person to the entitlement, every step with its `kind` and the `edge` that led to it), `truncated` and `orphan_accounts` |
| `GET /api/v1/identities/{id}/reach` | `{"targets": [...]}`: the targets that person reaches, each with their paths |
| `GET /api/v1/unused-access?identity=&limit=&after=` | `{"access": [...], "days": 90}`, paged by `after=<last id>` |
| `GET /api/v1/role-suggestions` | `{"suggestions": [...]}`: each with `conditions` ready for an access rule, `people`, `have_all` and its `entitlements` with their `holders` |

Review items (`GET /api/v1/review-items?scope=mine`) carry `suggestion` with `action`, `signals`, `last_used_at`, `tracked`, `peer_group`, `peers` and `peer_holders`.
