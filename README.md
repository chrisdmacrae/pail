# Pail

Pail hosts static sites, PWAs and small server apps on a homelab behind one REST API. Each hosted property is a **pail**: one name, one URL, one status.

The spec is the handoff doc; the look and copy come from the design system and the prototype:

- Handoff: https://claude.ai/code/artifact/7af6e2ae-d13d-4fb3-93b3-514b3882aa04
- Design system: https://claude.ai/code/artifact/99fe52c8-71ba-48f8-aacf-6998aae053f9 (copied into `web/design-system/`)
- Prototype: https://claude.ai/artifact/VNgqMbgLMgpHn5ETmGcSwp (source kept in `web/prototype/` for reference)

## Where the build is

| Step | What | State |
| --- | --- | --- |
| 1 | API, versitygw storage and Host-header routing for static pails | done |
| 2 | pail-cli with profiles: login, up, ls, logs | done |
| 3 | Deploy history and rollback | done |
| 4 | Web UI from the design system | done |
| 5 | Custom hostnames and the DNS self-check | done |
| — | TLS: Pail's own certificate authority, and Let's Encrypt by DNS-01 | done |
| 6 | Git hosts: token first, then OAuth | done |
| 7 | Firecracker microVMs: the build VM, containers, and the KVM check | next |
| 8 | pail.json functions: base images, snapshots, sleep when idle, and routing | |

## Layout

```
cmd/pail-server      the server binary
cmd/pail             pail-cli, installed as the pail command
internal/config      environment variables
internal/storage     the object store: S3 (versitygw) and an in-memory one for tests
internal/certs       TLS certificates: Pail's own authority, or Let's Encrypt
internal/githost     git hosts: GitHub, GitLab, Bitbucket, Gitea and Forgejo
internal/pails       pails, deploys, the live pointer, the deploy pipeline
internal/server      the listener: Host routing, the REST API, static serving
internal/cli         the pail command: profiles, packing, the API client
internal/webui       the built web UI, embedded into pail-server
web/ui               the web UI's source: Vite, React 18, TypeScript
docs                 the documentation site: Astro, with pages in docs/src/content/docs
web/design-system    tokens, components, fonts and brand marks, as published
web/prototype        the prototype's source, for matching screens and copy in step 4
```

## Run it

`make` lists every command. The first time:

```bash
make setup
```

That installs the UI's packages and a local versitygw. Then:

```bash
make dev
```

This runs versitygw, the server and the web UI together; Ctrl-C stops all three, and so does any one of them failing. The UI, with hot reload, is at http://localhost:5173. To run them in separate terminals instead, use `make dev-storage`, `make dev-server` and `make dev-ui`.

Point the CLI at it once:

```bash
PAIL_TOKEN=dev-token pail login http://localhost:8080 --profile dev
```

`dev-server` runs on `localhost:8080` with `localhost` as the base domain, so `<name>.localhost:8080` resolves without DNS, and its token is `dev-token`. `make install` puts the `pail` command on your path. `dev-server` builds the UI first, so the server also serves it, as of when it started, at http://localhost:8080.

| Command | What it does |
| --- | --- |
| `make build` | Builds `bin/pail-server`, with the web UI inside, and `bin/pail`. |
| `make check` | Lint, then every test. Run it before a commit. |
| `make test` · `make lint` · `make fmt` | Each on its own; `test-go`, `test-ui`, `lint-go` and `lint-ui` narrow them. |
| `make dev-ui` | The web UI with hot reload on `:5173`, using `dev-server`'s API. |
| `make dev-docs` | The documentation site with hot reload on `:4321`. |
| `make docs` | Builds the documentation site into `docs/dist`. |

`go build ./cmd/pail-server` on its own works too, but without `make ui` first the server has no web UI and says so at `/`.

## Web UI

The base domain serves the UI: your pails, a pail's page, and New pail. It is a plain client of the API below and asks for the installation's token once per browser.

- **Your pails:** the list, with Redeploy and Remove on each row.
- **Trust this Pail:** when the installation has its own authority, how each kind of device comes to trust it.
- **A pail:** its deploys and their logs (live while building), Upload a deploy (a `.zip` or a folder), Serve this one, Redeploy, Stop or Start, and Remove.
- **New pail:** the pail-cli commands; Upload, where you drop a folder or a `.zip` (a folder is packed into a `.tar.gz` in the browser); or a git host, where you connect with a token and pick a repo.

Not in the UI yet: the functions panel (step 8), and real install instructions for pail-cli.

It's built from `web/design-system/` as published: the components come from its `bundle.js`, and nothing in that folder is edited. Day or Night follows the device.

## pail-cli

| Command | What it does |
| --- | --- |
| `pail login <url>` | Checks the installation answers with the token, then saves a profile. The token comes from `PAIL_TOKEN`, or a hidden prompt on a terminal. Without `--profile`, the profile is named after the host (`pail.lan` becomes `pail-lan`). |
| `pail profiles` · `pail profiles use <name>` · `pail profiles rm <name>` | Lists profiles, sets the default, removes one. |
| `pail up [dir] [--name <pail>]` | Packs `dir` (default `.`), deploys it, follows the log on stderr and prints the URL on stdout. |
| `pail ls` | Every pail: name, status, URL, last deploy. |
| `pail logs <pail> [deploy] [--follow]` | A deploy's log; the latest by default. |
| `pail deploys <pail>` | The kept deploys, newest first, marking the one being served. |
| `pail rollback <pail> <deploy>` | Serves an older deploy. Nothing is rebuilt. |
| `pail redeploy <pail>` | Makes a new deploy from the latest good deploy's files and follows its log. |
| `pail stop <pail>` · `pail start <pail>` | Turns a pail Off or back on. It keeps its deploys. |
| `pail hosts <pail>` · `pail hosts add <pail> <hostname>` · `pail hosts rm <pail> <hostname>` | Lists, adds or removes custom hostnames, and says whether each points at the installation yet. |
| `pail open <pail>` | Opens the pail's URL in a browser, and prints it. |
| `pail ca` | Prints the installation's root certificate, when it has its own authority. |
| `pail rm <pail> [--yes]` | Removes a pail and all its deploys; asks first unless `--yes`. |

Every command takes `--profile`/`-p`, `--json`, `--quiet`/`-q` and `--yes`/`-y`.

- **Name:** `pail up` uses `--name`, else `name` in `pail.json`, else the name of the folder holding `.git`, else the current folder's name, stepping out of `dist`, `build`, `out`, `output`, `public`, `_site` and `www`.
- **What's packed:** every file under `dir` except `.git`, `node_modules` and `.DS_Store`.
- **Which installation:** `--profile`, then `PAIL_PROFILE`, then `PAIL_URL` with `PAIL_TOKEN` (no file needed), then `default` in the config, then the only profile. On a terminal each command first prints the one it picked to stderr.
- **Config:** `~/.pail/config` (TOML, mode 0600 in a 0700 folder); `PAIL_CONFIG` moves it. pail won't read it if other users can.
- **Trust:** when an installation has its own certificate authority, `pail login` takes its root from the TLS handshake, prints its SHA-256 fingerprint, saves it beside the config and sets the profile's `ca`. In CI, `PAIL_CA` names a root certificate file to trust, with `PAIL_URL` and `PAIL_TOKEN`.
- **Exit codes:** 0 done; 1 the deploy failed; 2 usage or config error; 3 installation unreachable; 4 pail or deploy not found; 5 the token was rejected.

## Configuration

Everything is an environment variable on the server.

| Variable | Default | What it sets |
| --- | --- | --- |
| `PAIL_TOKEN` | none (required) | The installation's secret access token. Pail refuses to start without it. |
| `PAIL_BASE_DOMAIN` | `pail.lan` | The domain every pail gets a name under. |
| `PAIL_MAX_UPLOAD_SIZE` | `100MB` | Largest archive accepted; bigger ones get a 413. |
| `PAIL_MAX_DEPLOYS` | `10` | Good deploys kept per pail for rollback. Failed ones don't count. |
| `PAIL_MAX_FUNCTION_MEMORY` | `1GB` | Reported by `/api/v1/info`; not enforced until functions exist. |
| `PAIL_ACME_DNS_PROVIDER` · `PAIL_ACME_DNS_TOKEN` | unset | Set both to get certificates from Let's Encrypt by DNS-01, and to allow custom hostnames. The provider is one of `bunny`, `cloudflare`, `desec`, `digitalocean`, `duckdns`, `gandi`, `hetzner`, `netlify`, `njalla`. |
| `PAIL_ACME_EMAIL` | unset | Optional address for Let's Encrypt's expiry notices. |
| `PAIL_ACME_DIRECTORY` | Let's Encrypt production | Another ACME directory, such as Let's Encrypt's staging one while you're trying things out. |
| `PAIL_ACME_RESOLVERS` | the system's | DNS servers to check the challenge record with, comma-separated, e.g. `1.1.1.1:53`. Set it when your home resolver answers for the domain itself and would never see the public record. |
| `PAIL_OAUTH_<HOST>_CLIENT_ID` · `_CLIENT_SECRET` | unset | An OAuth app for a git host (`GITHUB`, `GITLAB`, `BITBUCKET`, `GITEA`, `FORGEJO`), which puts "Sign in with …" on New pail. |
| `PAIL_OAUTH_<HOST>_SERVER` | gitlab.com for GitLab | Where the app is registered, for a host you run. Required for Gitea and Forgejo. |
| `PAIL_LISTEN` | `:80` | Address the plain-HTTP listener binds. |
| `PAIL_LISTEN_TLS` | `:443` | Address the HTTPS listener binds. |
| `PAIL_TLS` | on | `off` serves everything over plain HTTP: for development, or behind a proxy that terminates TLS itself. |
| `PAIL_S3_ENDPOINT` | none (required) | versitygw's URL, e.g. `http://versitygw:7070`. |
| `PAIL_S3_ACCESS_KEY` · `PAIL_S3_SECRET_KEY` | none (required) | versitygw credentials. |
| `PAIL_S3_BUCKET` | `pail` | Bucket Pail keeps everything in; created if missing. |
| `PAIL_S3_REGION` | `us-east-1` | Region sent with requests. |

## API so far

Every call sends `Authorization: Bearer <PAIL_TOKEN>`. The API answers on the base domain, and on the server's IP or `localhost`. Errors are `{"error": {"code", "message"}}`.

| Method and path | What it does |
| --- | --- |
| `GET /api/v1/info` | Version, base domain and limits. |
| `GET /api/v1/pails` | Every pail, most recently updated first. |
| `GET /api/v1/pails/{name}` | One pail. |
| `POST /api/v1/pails/{name}/deploys` | Body is a `.tar.gz` or `.zip`. Creates the pail on its first deploy. Answers `202` with the deploy as soon as the archive arrives. `?source=cli\|upload`, `?file=<name>` for the history label. |
| `GET /api/v1/pails/{name}/deploys/{id}/log` | Server-sent events: a `line` event per log line, then one `done` event with the finished deploy. `?follow=false` sends a building deploy's lines so far and stops. |
| `GET /api/v1/pails/{name}/deploys` | The kept deploys, newest first, each with `serving: true\|false`. |
| `POST /api/v1/pails/{name}/serve` | Body `{"deploy": "<id>"}`. Points the pail at a kept deploy that finished; answers with the pail. `409` while a deploy is running or if that deploy failed. |
| `POST /api/v1/pails/{name}/redeploy` | Starts a deploy that copies the latest good one. Answers `202` like an upload; `409` if no deploy has finished. |
| `POST /api/v1/pails/{name}/stop` · `/start` | Turns the pail Off or back on; answers with the pail. |
| `GET /api/v1/pails/{name}/hosts` | The pail's addresses, its own first, each with `points_here` from a fresh DNS check. |
| `POST /api/v1/pails/{name}/hosts` | Body `{"host": "recipes.home.example"}`. Adds a custom hostname and checks it. `409` unless Let's Encrypt mode is on, or if another pail has it. |
| `DELETE /api/v1/pails/{name}/hosts/{host}` | Removes a custom hostname. |
| `GET /api/v1/check` | The DNS self-check for the base domain: do names under it reach this Pail? |
| `GET /api/v1/git` | The five git hosts, and which are connected. |
| `PUT /api/v1/git/{kind}` | Body `{"token": "...", "server": "..."}`. Checks the token with the host, then keeps it. `server` is for GitLab, Gitea and Forgejo. |
| `DELETE /api/v1/git/{kind}` | Forgets a host's token. Its pails stay, but can't pull. |
| `POST /api/v1/git/{kind}/oauth` | Begins signing in to a host that has an OAuth app set up. Answers with the address to send the browser to; the host sends it back to `/oauth/callback/{kind}`. |
| `GET /api/v1/git/{kind}/repos` | The repos the token can see. |
| `GET /api/v1/git/{kind}/detect?repo=&branch=` | What Pail makes of a repo, and whether it can deploy it. |
| `POST /api/v1/pails/{name}/repo` | Body `{"host", "repo", "branch"}`. Makes a new pail from a repo, deploys the branch, and adds a webhook so pushes deploy. |
| `POST /api/v1/hooks/{name}` | Where a git host delivers pushes. Takes no token: the delivery is signed with the hook's secret. |
| `DELETE /api/v1/pails/{name}` | Removes the pail and every deploy. |

## How a request is routed

- `<name>.<base domain>` is that pail's site, served from its live deploy. So is any custom hostname added to the pail.
- The base domain itself, an IP or `localhost` is the installation: the API under `/api/v1`, the web UI everywhere else.
- Any other host gets a plain 404 naming the installation.
- A pail that is Off answers every request with a plain 503 saying so.

On a pail's site, `/` and `/dir/` serve `index.html`, `/dir` redirects to `/dir/`, `/about` serves `about.html` if there is one, and a miss serves the deploy's `404.html` or, with a `fallback` in `pail.json`, that file.

## Git hosts

A pail can come from a repo on GitHub, GitLab, Bitbucket, Gitea or Forgejo. Pail holds one access token per host, checks it with the host before keeping it, and stores it in the object store under `git/`.

- **What Pail deploys.** A repo's files as they are: an `index.html` at the top, or a `pail.json` that says where the files live. A repo whose `package.json` has a build script is recognised and declined, because Pail can't run builds until step 7.
- **Pushes deploy.** When a pail is made from a repo, Pail adds a webhook to it. Each push to the pail's branch is fetched and deployed like any other deploy. The host has to be able to reach Pail for this; a host on the internet can't reach a Pail that is only on your network.
- **If the webhook can't be added,** the pail is still made and says so. Redeploy pulls the branch by hand: `pail redeploy <pail>`.
- **Tokens.** For Bitbucket, an access token, or `email:api-token`. For the others, a personal access token that can read repos and add webhooks.
- **Signing in.** With an OAuth app set up for a host (`PAIL_OAUTH_<HOST>_CLIENT_ID` and `_CLIENT_SECRET`), New pail offers "Sign in with …" above the token form. The app's callback address is the one you open Pail at, followed by `/oauth/callback/<host>`. Pail renews a sign-in's token by itself when the host issues ones that run out. Signing in happens in the browser, so it works on a Pail only your network can reach.

The documentation site has a guide for each host under "Set up a git provider".

## Hostnames and the DNS check

Pail routes by the Host header, so every pail name and custom hostname has to resolve to this server. Pail checks that by asking for a one-time path, `/.well-known/pail/<nonce>`, at the hostname and seeing whether the request comes back to itself. It never needs to know its own outside address, so the check works the same behind Docker port mapping, in an LXC or in a VM.

- **On start**, Pail checks a random name under the base domain and logs whether it arrived.
- **In the UI**, Your pails shows a note when names under the base domain don't reach Pail, and a pail's Addresses show "Points here" or "Not pointing here yet" for each hostname.
- **`pail hosts`** reports the same.

A hostname is added whether or not its DNS is ready; it starts answering as soon as it points here. Custom hostnames need Let's Encrypt mode (see TLS).

## TLS

Pail serves every pail over HTTPS from its own listener. The plain listener answers only the DNS self-check and `/ca.crt`, and redirects everything else to HTTPS. There are two ways Pail gets certificates.

**Pail's own authority (the default).** On first start Pail makes a root certificate, valid ten years, that can only sign for the base domain: it carries a critical name constraint, so even its key can't be used to impersonate another site. From it Pail issues a wildcard for the base domain that lasts a week and is replaced at half-life; nobody handles those. Each device trusts the root once: it's at `http://<base domain>/ca.crt`, `pail ca` prints it, and the web UI's "Trust this Pail" page has the steps per platform. This mode has no custom hostnames.

**Let's Encrypt (when `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` are set).** The base domain must be a real one you control. Pail proves ownership by having the DNS provider publish a TXT record, so it works on a server with private addresses and no open ports. It gets a wildcard for the base domain, and a certificate of its own for each custom hostname, and renews them when a third of their life is left.

- **First start waits.** Pail doesn't begin serving until it has the wildcard, and refuses to start, saying why, if it can't get one.
- **Adding a hostname waits too.** `pail hosts add` and the UI return once the hostname's certificate is issued. If Let's Encrypt won't issue one, because the hostname isn't in a zone the token can edit, the hostname isn't added.
- **Try it on staging first.** Let's Encrypt's production directory has rate limits. Set `PAIL_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory` until it works, then remove it.

The base domain needs at least two labels (`pail.lan`, not `localhost`): browsers refuse a wildcard certificate directly under a single-label name. `make dev` runs with `PAIL_TLS=off` for that reason.

The root's key, the ACME account and the certificates are kept in the object store under `tls/`, beside everything else Pail stores.

## Storage layout

```
pails/<name>/deploys/<id>/...             the unpacked upload, never changed
meta/<name>/state.json                    the pail and its live pointer
meta/<name>/deploys/<id>.json             the deploy record
meta/<name>/deploys/<id>.log              its log
meta/<name>/deploys/<id>.manifest.json    what it serves
git/<host>.json                           a git host's server and access token
tls/internal/root.pem                     Pail's own root certificate and its key
tls/acme/<directory>/account.json         the Let's Encrypt account
tls/acme/<directory>/certs/<name>.pem     each certificate and its key
```

Going live is one write of `state.json`. If anything fails before it, the pointer never moves. A rollback is the same write, aimed at an older deploy.

Pail keeps the newest `PAIL_MAX_DEPLOYS` good deploys per pail for rollback and deletes the rest oldest first. The deploy being served is never deleted, however old. Failed deploys are counted apart and never take a good deploy's place: they hold no files, only a record and a log, and Pail keeps the newest `PAIL_MAX_DEPLOYS` of those too. A pail whose latest deploy failed shows Failed until a deploy succeeds or you roll back.

## Test

```bash
make check
```

## Documentation site

`docs/` is a static site built with Astro and styled from `web/design-system/`. Every page is a Markdown file in `docs/src/content/docs`, an Astro content collection: a file's path is its URL, and its front matter (title, lead, section, order) puts it in the sidebar. Adding a page is adding a file.

```bash
make dev-docs
```
