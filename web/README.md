# Grantra console

The web console: React, TypeScript, Vite, TanStack Query and Tailwind, with
small [shadcn/ui](https://ui.shadcn.com)-style components in
`src/components/ui`. The Go core serves the built files from the same origin
as the API, so the browser only ever holds the `HttpOnly` session cookie.

## Develop

Start the dev stack (`make up` from the repository root) or any Grantra core
on port 8080, then:

```sh
cd web
npm ci
npm run dev   # http://localhost:5173, proxies /api and /auth to :8080
```

To sign in through the Vite server, run the core with
`GRANTRA_PUBLIC_URL=http://localhost:5173` so the login provider sends you back
to Vite. The bundled Keycloak realm already allows that redirect. Point the
proxy elsewhere with `GRANTRA_CORE_URL`.

## Check

```sh
npm run lint       # ESLint + Prettier
npm run typecheck
npm test           # Vitest + Testing Library
npm run build      # writes dist/
```

`make web` from the repository root builds the console and copies it into
`core/internal/webui/dist`, where the Go binary embeds it. The Docker image
does the same in its first stage.
