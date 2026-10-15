# Grantra Login

Grantra Login is the sign-in service that ships with Grantra: [Keycloak](https://www.keycloak.org/) with Grantra's look, governed by Grantra. It is for companies without an identity provider, or that want to leave theirs. Companies on Entra ID, Okta or similar keep signing in there and point Grantra at it instead (see [production.md](production.md)).

## Theme

The `grantra` theme in [`deploy/keycloak/themes/grantra`](../deploy/keycloak/themes/grantra) restyles Keycloak's own pages and emails, so people see Grantra Login and not Keycloak:

- **Sign-in pages** (`login`): the stock `keycloak.v2` pages (sign in, password reset, one-time codes, passkeys) with Grantra's colors, logo and footer. Light and dark follow the browser.
- **Emails** (`email`): Keycloak's texts in a Grantra layout, with a friendlier "set up your account" email for new users.
- English and Spanish; the page follows the browser's language, with a language menu.

It is plain CSS, FreeMarker and message files on top of the built-in themes, with no build step. Keycloak updates keep working: the theme only overrides styles, the footer, the email layout and a few texts.

Where it comes from:

| Where | How |
|---|---|
| docker-compose | The theme folder is mounted into the stock Keycloak image. `start-dev` does not cache themes, so edits show on the next page load |
| Helm | The chart runs `ghcr.io/grantra-dev/grantra-login`, built from [`deploy/keycloak/Dockerfile`](../deploy/keycloak/Dockerfile) and released, signed, with each Grantra version |

The bundled realm ([`grantra-realm.json`](../deploy/keycloak/grantra-realm.json)) selects it with `"loginTheme": "grantra"` and `"emailTheme": "grantra"`. For another realm, pick *grantra* under **Realm settings** → **Themes**.

## The console page

**Administration** → **Grantra Login** manages the realm behind the application that uses the `grantra-login` connector (the oldest one, if there are several). Admins change things; auditors can look. Every change is in the audit log (`login.*` events).

### Apps that sign in with Grantra Login

The page lists the OpenID Connect apps registered in the realm, with their redirect URIs, and registers new ones: a name, a client ID and the redirect URIs. Apps sign people in with the authorization code flow and PKCE. Redirect URIs must be `https` (or `http://localhost` for development) and have no wildcards, since a URI that matches more than the app is how sign-in codes get stolen. A confidential app's secret is shown once, when it is created or replaced with **New secret**; the old secret stops working right away. Public apps (single-page and mobile apps) have no secret. Keycloak's own clients and Grantra's service account are not listed and cannot be changed from here.

### Sign-in policy

| Ask for a second factor | What Grantra writes in Keycloak |
|---|---|
| Only for people who set one up (default) | Keycloak's own `browser` flow, untouched |
| For people with sensitive access | A `grantra browser` flow that asks for a one-time code from whoever has the `grantra-mfa-required` realm role, and keeps asking anyone else who set one up. Grantra keeps the role in step with access (below) |
| For everyone | A `grantra browser` flow that asks everyone for a one-time code; people without an authenticator app set one up at their next sign-in |

The `grantra browser` flow is a copy of Keycloak's `browser` flow with its "Conditional 2FA" step changed, so cookies, identity providers and organizations keep working. Grantra owns it and rebuilds it on every policy change: edits made to it in Keycloak are lost. The second factor is a one-time code from an authenticator app (FreeOTP, Google or Microsoft Authenticator), because Keycloak only walks people through setting up a factor that is required, not one of several.

**Passkeys** turns on Keycloak's passkey sign-in: people add a passkey from their account page, then sign in with it, without a password or a code. A passkey already proves two things (the device and the fingerprint or PIN), so under "For people with sensitive access" and "For everyone" signing in with one skips the one-time code.

### Risk-based MFA

Under "For people with sensitive access", Grantra decides who has to use a second factor, because it knows who holds what. Every five minutes, and right after the policy is saved, it gives the `grantra-mfa-required` realm role to the Grantra Login account of every active person who holds, or is assigned, an entitlement with risk `high` or `critical` in any application (Grantra admins included), and takes it from everyone else. Mark entitlements' risk in the catalog to choose who that is. Each change is audited as `login.mfa_required` or `login.mfa_not_required`. The role belongs to Grantra: Grantra does not read it as an entitlement, and people added to it by hand are taken out.

## Bring your own identity provider

Grantra Login is optional. Companies on Entra ID, Okta, Google or another OpenID Connect provider leave it off (`login.enabled: false` in the Helm chart, the default) and point the console at their provider with `oidc.issuer`, `oidc.clientId` and `oidc.clientSecret` (see [production.md](production.md)). Grantra then governs on top of that provider through its connectors (SCIM for Entra ID and Okta, LDAP for Active Directory), and this page says Grantra Login is not connected.

## Governing it

Grantra Login is an application like any other in Grantra, read and provisioned by the [`grantra-login` connector](connectors.md#grantra-login): its users are accounts, its groups and realm roles are entitlements. With provisioning on, joiners get an account and an email to set their password, movers' groups follow their access, and leavers are disabled and signed out.
