# Insights

**Governance → Insights** shows what the access data says beyond who holds what. Admins act on it; auditors can look at everything. Nothing here changes access on its own.

## Unused access

Access someone was given by hand or by request, or may turn on just in time, and has not used in **90 days**. Birthright access is left out: a rule gives it, so the rule is what to change.

Grantra knows when access is used for:

- The **vault**, **servers** and the **gateway**: people go through Grantra.
- Access people **turn on** just in time.
- **Grantra** itself: signing in to the console.
- Applications whose accounts say when they **last signed in**: Active Directory (`lastLogonTimestamp`), OpenLDAP with the password policy overlay (`pwdLastSuccess`), or an [account file](csv-files.md) with a `last_login` column.
- Applications whose **sign-ins** reach Grantra (Grantra Login, or a script sending them), when the sign-in names the application as Grantra calls it.

Where Grantra cannot tell, the access is never called unused. For each one, **Make it just in time** turns standing access into an eligibility, and **Remove eligibility** takes away what nobody turns on.

The same findings show next to each access in **Access reviews**, with how many of the person's peers hold it, so reviewers see what deserves a closer look.

## Role suggestions

Grantra looks in every department, and every job title within one, for access at least **80%** of the people there hold one by one: by hand, by request, or in the application without Grantra giving it. Each suggestion says how many people the group has, how many hold all of it already and how many hold each entitlement.

Pick the entitlements to keep, name the role and **Create role**. With **Give it to everyone in…** checked, a birthright rule gives the role to the whole group, and to whoever joins it. A review then looks at one rule instead of every grant.

Groups of fewer than 5 people say nothing, just-in-time only access never goes in a role (the role would make it standing), and what a role or a rule already gives is left out.
