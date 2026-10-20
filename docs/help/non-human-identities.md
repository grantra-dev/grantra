# Service accounts and agents

Programs hold access too: service accounts that sync data, AI agents that read the CRM, workloads that call other services. Nobody hires or fires them, so their access piles up. In Grantra each one is an identity of its own with an **owner**, the person who answers for it.

## Kinds

- **Service account**: a login a program uses in an application, such as `svc-backup` in Active Directory.
- **AI agent**: an assistant or automation that acts in applications, such as a support bot reading tickets.
- **Workload**: a service identified by its [SPIFFE ID](https://spiffe.io) (`spiffe://example.org/ns/prod/sa/billing`) rather than by a password. Your applications can ask Grantra whether a workload may do something by that ID.

## What the owner does

The owner is to a service account what a manager is to a person:

- They **review its access** in campaigns by manager.
- They **approve what it asks for**, and can request access for it.
- They see it, its access and its accounts.

When the owner leaves the company, each one they owned passes to their **manager**, who gets an email saying so. If the owner had no manager, it is left with **no owner** for an admin to sort out.

A rule for everyone (a birthright rule with no conditions, or one by department) never gives access to a service account, agent or workload: only a rule that names their kind does.

## Governance → Service accounts and agents

Admins add them and change them; auditors and helpdesk look. Each one shows its owner, its accounts and when it last signed in, and is flagged when:

| Flag | Means |
|---|---|
| **No owner** | Nobody active answers for it |
| **Secret outside the vault** | An account with high or critical risk access keeps its password outside the vault, so nobody knows who has it or when it changed |
| **Unused** | Its accounts or sign-ins say it has not signed in for 90 days |
| **Ends soon** | Its end date is within 30 days |

An **end date** is worth setting for anything temporary, such as an agent being tried out: on that day Grantra retires it on its own. **Retire** does the same at once: Grantra removes the access it gave and, where provisioning is on, disables its accounts.

## Accounts nobody owns

The second tab lists active accounts that no person or program in Grantra owns, the ones whose name looks like a program's (`svc`, `bot`, `api`, `deploy`...) first, then the ones with high or critical risk access. **Give it an owner** makes it a service account, agent or workload with that account. If an account is a person's, link it from the application's page instead.
