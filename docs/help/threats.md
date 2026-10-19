# Threats

**Security → Threats** shows what Grantra found that looks like someone misusing an identity: a stolen password, an account that should be off, access given behind Grantra's back. Admins act on it; auditors can look at everything.

## What Grantra looks at

Every minute, Grantra looks at the new **sign-ins** and the new **access** since the last time:

- Sign-ins to **Grantra Login** are read on their own.
- Sign-ins to another provider (Entra ID, Okta, Google) arrive when a script sends them with an [API token](administration.md) that may *send sign-ins*. The **Sign-ins** tab says which sources sent what in the last day, and how many matched a person.
- Access comes from the applications Grantra already reads, and from the audit log.

## Alerts

Each alert says what happened, to whom, and the evidence: addresses, countries, how many tries. If the same thing goes on, the alert counts it instead of opening another, and becomes more severe when it gets worse (a wrong password streak followed by a sign-in, for example). High and critical alerts also go to the admins' **Inbox** and by email.

Nobody is told about, or can act on, an alert about themselves: whoever took over their account would see it too. Another admin looks into it.

## What you can do

Open an alert, look at the evidence and choose:

| Action | What it does |
|---|---|
| **End sessions** | Signs the person out of Grantra Login and the console, ends their server and gateway sessions and checks in what they took from the vault. AWS console sessions last until they expire on their own. |
| **Require a second factor** | Asks for a one-time code at every sign-in for some days, and signs them out so it applies now. Needs a Grantra Login sign-in policy that asks for a second factor (*For people with sensitive access* or *For everyone*). |
| **Suspend** | Ends their sessions and turns their Grantra Login account off; applications Grantra provisions turn their accounts off too. Their access in Grantra is kept, so **Reinstate** gives everything back. The last active admin cannot be suspended. |

Then **Resolve** the alert when it is dealt with, or **Dismiss** it with a note when it was not a threat (they really are travelling). Everything is in the audit log.

## Detections

The **Detections** tab lists what Grantra looks for. Each one can be turned off, given another severity and tuned, like how many wrong passwords count as guessing.

A detection can also **respond on its own**, so that Grantra ends sessions or requires a second factor the minute it finds the threat, without waiting for an admin. Only the responses that are safe for that detection are offered: detections that only see wrong passwords never suspend anyone, or anybody could lock a colleague out by typing wrong passwords in their name.

| Detection | What it means |
|---|---|
| Leaver signed in | Someone who left, or was suspended, signed in: an account that should be off still works |
| Impossible travel | Two sign-ins too far apart for the time between them |
| New country | A sign-in from a country this person does not sign in from |
| Dormant account used | A sign-in after months without one |
| Password guessing | Many wrong passwords for one person |
| Password spray | One address trying passwords for many people |
| MFA fatigue | The second factor refused again and again: someone with the password pushing prompts |
| Privilege granted outside Grantra | High or critical access appeared in an application and nobody asked for it in Grantra |
| New Grantra admin | Someone was made a Grantra admin |

A company that does not want any of this can turn it off in **Settings → Modules**; Grantra then stops looking.
