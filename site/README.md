# grantra.dev

The landing page, plain HTML and CSS with no build step.

Preview it locally with any static server, for example `python3 -m http.server -d site`.

## Deploy on Cloudflare Pages

Create a Pages project connected to this repository with:

- Production branch: `main`
- Framework preset: None
- Build command: empty
- Build output directory: `site`

Then add the custom domain `grantra.dev` in the project's **Custom domains** tab.
