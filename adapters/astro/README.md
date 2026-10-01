# astro-pail

Deploys an [Astro](https://astro.build) site to [Pail](https://github.com/chrisdmacrae/pail). Prerendered pages and assets are served as files. Pages rendered on demand, API routes, actions and server islands are answered by one Pail function.

The guide is in Pail's docs, under Deploying: `docs/src/content/docs/deploying/astro.md`.

## Use it

```bash
npm install astro-pail
```

```js
// astro.config.mjs
import { defineConfig } from 'astro/config';
import pail from 'astro-pail';

export default defineConfig({
  adapter: pail(),
});
```

Then `pail up` from the project. Pail builds it and reads the `pail.json` the build leaves in `dist`. To build on your own machine instead, run `astro build`, then `pail up dist`.

## Options

| Option | Default | What it sets |
| --- | --- | --- |
| `name` | `ssr` | The function's name in `pail.json`. |
| `memory` | `256MB` | How much memory each copy of the function gets. |
| `timeout` | Pail's, `10s` | How long one request may take. |
| `idle` | Pail's, `5m` | How long a copy waits for another request before it sleeps. |
| `max` | Pail's, `4` | How many copies may run at once. |
| `env` | none | Environment variables for the function. |
| `external` | `[]` | Packages to leave out of the function's files, for Pail to install where it runs. `sharp` always is. |

## How it works

- `src/index.js` is the integration. After the build it writes `dist/pail.json`: `static` is Astro's `client` folder, the function's `src` is its `server` folder, and the routes send each file's path to files and everything else to the function.
- `src/routes.js` works those routes out. A route's path in `pail.json` covers itself or, with a star on the end, everything under it, and the first that fits wins. A folder no on-demand route can reach gets one route; any other is listed file by file.
- `src/server.js` is the function: built into `dist/server/entry.mjs`, with the site's packages inside it. Pail runs it once for each request, as CGI. `src/cgi.js` turns CGI's variables into a `Request`, and a `Response` back into CGI.
- `src/emulate.js` answers a build the way Pail would, running the function once for each request. `astro preview` uses it, and so do the tests.

## Develop it

```bash
pnpm install
pnpm test    # builds the sites in test/fixtures and asks them for pages
pnpm lint
```

From the top of the repo, `make test-astro` and `make lint-astro` do the same.

## Releases

Any push to `main` that changes `src` or `package.json` here publishes `astro-pail` to npm, with no tag. A change to the tests or this file alone doesn't. `.github/workflows/release-astro.yml` lints, tests and publishes it.

The version is picked by `scripts/npm-next-version`. The one in `package.json` is used the first time; once it is on npm, each release is the next patch. To start a new minor or major, change `package.json`. The version a release takes isn't written back here, so `package.json` names the line being released, not the latest version.

Publishing needs an npm access token that may publish `astro-pail`, as the repository secret `NPM_TOKEN`.
