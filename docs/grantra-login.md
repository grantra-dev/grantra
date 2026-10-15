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

### Identity providers

Companies that already have Entra ID, Okta or another provider can keep it and still use Grantra Login: **Add identity provider** connects one (Keycloak identity brokering), and people sign in through it.

| Kind | What it asks for | Register at the provider |
|---|---|---|
| Microsoft Entra ID | Your tenant id or a domain the tenant owns, and an app registration's client ID and secret | A web app with the redirect URI the page shows |
| Okta | Your Okta domain (such as `acme.okta.com`) and an OIDC app's client ID and secret | An OIDC web app with the redirect URI the page shows |
| OpenID Connect | The issuer URL, client ID and secret | A confidential client with the redirect URI the page shows |
| SAML | The provider's metadata URL, and the attribute that carries the email (`email` by default) | An app from Grantra Login's SAML metadata, linked on the page, sending the email as the NameID |

Grantra Login reads the provider's configuration (its discovery document or metadata) when the provider is added, so the provider must be reachable from Grantra Login. Entra ID must be your own tenant: the multi-tenant endpoints (`common`, `organizations`) accept accounts from any tenant, whose emails nobody here vouches for. The client secret goes to Keycloak and is not stored in Grantra or the audit log.

With **Link by email** (the default), someone whose provider sends the email of an existing Grantra Login account signs in to that account straight away; anyone else gets a new account, which the [`grantra-login` connector](connectors.md#grantra-login) reads like any other. Grantra writes a `grantra first broker login` flow for this: Keycloak's `first broker login` with "Automatically set existing user" in place of "confirm, then prove the account is yours". It trusts the provider's emails, so only use it with a provider that only sends emails it verified, such as your own Entra ID tenant or Okta org. Without it, Keycloak asks people to confirm with their Grantra Login password the first time.

### Moving over one domain at a time

Each identity provider can have **email domains**. People whose email is in one of them go straight to that provider when they sign in; everyone else signs in with their Grantra Login password. So a company can start with every domain on its old provider, then move a domain at a time by taking it off the provider, once its people have a Grantra Login password (the [import](#importing-from-another-keycloak) keeps the passwords they had, or the setup email sets one). A domain goes to one provider only.

Grantra keeps a Keycloak organization per provider (`grantra-<alias>`) holding its domains. While any organization exists, the sign-in page asks for the email first and then for the password, which is how Keycloak picks the provider; Grantra turns organizations off again when the last domain is taken away.

### Importing from another Keycloak

**Import from Keycloak** moves users, groups, realm roles and apps (clients, with their roles and secrets) from another Keycloak. Export the realm with its users in the same file:

```sh
kc.sh export --realm acme --users realm_file --file acme.json
```

The page first shows what the import would add and what it skips because Grantra Login already has it (by username, group name, role name and client ID), then imports on **Import**. Nothing that exists already changes. People keep their passwords and authenticator apps, since the export carries their hashes, and their group and role memberships. Left out, and listed in the preview: Keycloak's own clients, service accounts, client scopes (Grantra Login's own apply), links to the old realm's identity providers (people link again by email), and the old realm's user federation (people federated from LDAP need a password reset, or the directory connected here). Imports are audited as `login.imported`. Files are up to 32 MiB.

## Bring your own identity provider

Grantra Login is optional. Companies that want Grantra Login's sign-in policies but keep their provider [federate it](#identity-providers). Companies on Entra ID, Okta, Google or another OpenID Connect provider leave it off (`login.enabled: false` in the Helm chart, the default) and point the console at their provider with `oidc.issuer`, `oidc.clientId` and `oidc.clientSecret` (see [production.md](production.md)). Grantra then governs on top of that provider through its connectors (SCIM for Entra ID and Okta, LDAP for Active Directory), and this page says Grantra Login is not connected.

## Governing it

Grantra Login is an application like any other in Grantra, read and provisioned by the [`grantra-login` connector](connectors.md#grantra-login): its users are accounts, its groups and realm roles are entitlements. With provisioning on, joiners get an account and an email to set their password, movers' groups follow their access, and leavers are disabled and signed out.
