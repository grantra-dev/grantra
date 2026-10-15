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

## Governing it

Grantra Login is an application like any other in Grantra, read and provisioned by the [`grantra-login` connector](connectors.md#grantra-login): its users are accounts, its groups and realm roles are entitlements. With provisioning on, joiners get an account and an email to set their password, movers' groups follow their access, and leavers are disabled and signed out.
