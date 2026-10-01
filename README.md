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
| 7 | Firecracker microVMs: the build VM, containers, and the KVM check | done |
| 8 | pail.json functions: base images, snapshots, sleep when idle, and routing | done |
| 9 | Running without KVM: one image, and Docker or Podman's containers in place of microVMs | done |

## Layout

```
cmd/pail-server      the server binary
cmd/pail             pail-cli, installed as the pail command
internal/config      environment variables
internal/storage     the object store: S3 (versitygw, or another store) and an in-memory one for tests
internal/certs       TLS certificates: Pail's own authority, or Let's Encrypt
internal/githost     git hosts: GitHub, GitLab, Bitbucket, Gitea and Forgejo
internal/microvm     Firecracker microVMs: base images, networking, builds, containers
internal/engine      the same, in the containers of Docker or Podman, where there is no KVM
scripts/kvm-host     the KVM host for development: a Lima VM, or a Linux machine over SSH
scripts/container-smoke runs Pail in a container and deploys one of everything to it
scripts/release      builds what a release publishes
scripts/brew-formula prints the Homebrew formula for a release
scripts/release-notes prints the install instructions a release's description opens with
scripts/npm-next-version prints the version astro-pail is published to npm as next
scripts/crate-next-version prints the version pail-fn is published to crates.io as next
deploy/proxmox       the installer that sets Pail up in a Proxmox container
deploy/container     Pail's image, and the script that runs it on Docker or Podman
.github/workflows    CI, the release that tags publish, astro-pail's release to npm, and pail-fn's to crates.io
internal/pails       pails, deploys, the live pointer, the deploy pipeline
internal/server      the listener: Host routing, the REST API, static serving
internal/cli         the pail command: profiles, packing, the API client
internal/webui       the built web UI, embedded into pail-server
web/ui               the web UI's source: Vite, React 18, TypeScript
docs                 the documentation site: Astro, with pages in docs/src/content/docs
adapters/astro       astro-pail, the Astro adapter: on-demand pages and API routes as a function
sdk/rust             pail-fn, the Rust crate: the request and response a Rust function is handed
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
| `make test-astro` · `make lint-astro` | The Astro adapter's tests, which build the sites in `adapters/astro/test/fixtures`, and its lint. |
| `make dev-docs` | The documentation site with hot reload on `:4321`. |
| `make docs` | Builds the documentation site into `docs/dist`: its pages as files, the function its search runs on, and the `pail.json` that says which is which. `pail up docs/dist` deploys it. |

`go build ./cmd/pail-server` on its own works too, but without `make ui` first the server has no web UI and says so at `/`.

## Web UI

The base domain serves the UI: your pails, a pail's page, and New pail. It is a plain client of the API below and asks for the installation's token once per browser.

- **Your pails:** the list, with Redeploy and Remove on each row.
- **Trust this Pail:** when the installation has its own authority, how each kind of device comes to trust it.
- **A pail:** its deploys and their logs (live while building), Upload a deploy (a `.zip` or a folder), Serve this one, Redeploy, Stop or Start, and Remove.
- **New pail:** the pail-cli commands; Upload, where you drop a folder or a `.zip` (a folder is packed into a `.tar.gz` in the browser); or a git host, where you connect with a token and pick a repo.


It's built from `web/design-system/` as published: the components come from its `bundle.js`, and nothing in that folder is edited. Day or Night follows the device.

## pail-cli

Install it with Homebrew, on macOS or Linux:

```bash
brew install chrisdmacrae/tap/pail
```

Or download it for your system, Windows included, from the [latest release](https://github.com/chrisdmacrae/pail/releases/latest) and put it on your `PATH`. From a checkout, `make install` builds it.

| Command | What it does |
| --- | --- |
| `pail login <url>` | Checks the installation answers with the token, then saves a profile. The token comes from `PAIL_TOKEN`, or a hidden prompt on a terminal. Without `--profile`, the profile is named after the host (`pail.lan` becomes `pail-lan`). |
| `pail profiles` · `pail profiles use <name>` · `pail profiles rm <name>` | Lists profiles, sets the default, removes one. |
| `pail up [dir] [--name <pail>]` | Packs `dir` (default `.`), deploys it, follows the log on stderr and prints the URL on stdout. |
| `pail ls` | Every pail: name, status, URL, last deploy. |
| `pail logs <pail> [deploy] [--follow]` | A deploy's log; the latest by default. |
| `pail logs <pail> --output [--follow]` | What the pail's containers are printing. |
| `pail deploys <pail>` | The kept deploys, newest first, marking the one being served. |
| `pail rollback <pail> <deploy>` | Serves an older deploy. Nothing is rebuilt. |
| `pail redeploy <pail>` | Makes a new deploy from the latest good deploy's files and follows its log. |
| `pail stop <pail>` · `pail start <pail>` | Turns a pail Off or back on. It keeps its deploys. |
| `pail env <pail>` · `pail env set <pail> NAME=value… [--secret]` · `pail env rm <pail> NAME…` | Lists, sets or removes a pail's variables, which `pail.json` uses as `${NAME}`. `--secret` keeps them sealed, never to be shown again. A `NAME` with no `=value` is asked for without showing it, or read from stdin. |
| `pail hosts <pail>` · `pail hosts add <pail> <hostname>` · `pail hosts rm <pail> <hostname>` | Lists, adds or removes custom hostnames, and says whether each points at the installation yet. |
| `pail open <pail>` | Opens the pail's URL in a browser, and prints it. |
| `pail ca` | Prints the installation's root certificate, when it has its own authority. |
| `pail rm <pail> [--yes]` | Removes a pail and all its deploys; asks first unless `--yes`. |

Every command takes `--profile`/`-p`, `--json`, `--quiet`/`-q` and `--yes`/`-y`.

- **Name:** `pail up` uses `--name`, else `name` in `pail.json`, else the name of the folder holding `.git`, else the current folder's name, stepping out of `dist`, `build`, `out`, `output`, `public`, `_site` and `www`. A folder inside a repo that isn't its top is named for both, so `pail up apps/web` in a repo called `acme` deploys `acme-web`, and two folders of one repo don't deploy over each other.
- **What's packed:** every file under `dir` except `.git`, `node_modules` and `.DS_Store`.
- **Workspaces:** a project that has a build script and no lockfile of its own, in a git repo with a lockfile in a folder above it, builds from that lockfile. `pail up` then packs from the folder with the lockfile and tells the server which folder to deploy, so the build has the lockfile and the packages beside the project. It says so when it does. An installation that can't build is sent the folder alone.
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
| `PAIL_MAX_FUNCTION_MEMORY` | `1GB` | The most memory one copy of a function may ask for in `pail.json`. A deploy that asks for more fails before anything is built. |
| `PAIL_MAX_CONTAINER_MEMORY` | `2GB` | The most memory one container may ask for in `pail.json`. A deploy that asks for more fails before anything is built. |
| `PAIL_SECRETS_KEY` | a key Pail makes | What pails' secrets are sealed with: any text, long and random for preference. Left unset, Pail makes a key the first time it starts and keeps it in `secrets.key` in `PAIL_DATA_DIR`. Either way, back it up: without it secrets can't be read. |
| `PAIL_ALLOW_LAN` | unset | Pails whose containers may reach the home network, by name with commas between: `media,backups`. Every other pail's can't. For code you trust with your network, like an app that mounts a share from a NAS. |
| `PAIL_ACME_DNS_PROVIDER` · `PAIL_ACME_DNS_TOKEN` | unset | Set both to get certificates from Let's Encrypt by DNS-01, which also allows custom hostnames. The provider is one of `bunny`, `cloudflare`, `desec`, `digitalocean`, `duckdns`, `gandi`, `hetzner`, `netlify`, `njalla`. |
| `PAIL_ACME_EMAIL` | unset | Optional address for Let's Encrypt's expiry notices. |
| `PAIL_ACME_DIRECTORY` | Let's Encrypt production | Another ACME directory, such as Let's Encrypt's staging one while you're trying things out. |
| `PAIL_ACME_RESOLVERS` | the system's | DNS servers to check the challenge record with, comma-separated, e.g. `1.1.1.1:53`. Set it when your home resolver answers for the domain itself and would never see the public record. |
| `PAIL_OAUTH_<HOST>_CLIENT_ID` · `_CLIENT_SECRET` | unset | An OAuth app for a git host (`GITHUB`, `GITLAB`, `BITBUCKET`, `GITEA`, `FORGEJO`), which puts "Sign in with …" on New pail. |
| `PAIL_OAUTH_<HOST>_SERVER` | gitlab.com for GitLab | Where the app is registered, for a host you run. Required for Gitea and Forgejo. |
| `PAIL_DATA_DIR` | `/var/lib/pail` | Local disk for what builds, containers and functions need: root filesystems, work disks, and containers' data volumes. |
| `PAIL_RUNTIME` | `auto` | What builds, containers and functions run in: `firecracker` for microVMs, `container` for the containers of Docker or Podman, or `auto` for microVMs where this machine can run them and containers where it can't. |
| `PAIL_FIRECRACKER` | `firecracker` | The Firecracker binary. |
| `PAIL_KERNEL` | `<data dir>/vmlinux` | The guest kernel every microVM boots. |
| `PAIL_CONTAINER_SOCKET` | `/var/run/docker.sock` | The container engine's API socket: Docker's, or Podman's, which answers the same API. |
| `PAIL_CONTAINER_NETWORK` | `pail` | The engine's network Pail's containers join, which Pail has to be on too. It also names this Pail's containers, images and volumes among the engine's others. |
| `PAIL_LISTEN` | `:80` | Address the plain-HTTP listener binds. |
| `PAIL_LISTEN_TLS` | `:443` | Address the HTTPS listener binds. |
| `PAIL_TLS` | on | `off` serves everything over plain HTTP: for development, or behind a proxy that terminates TLS itself. Custom hostnames are allowed, since the proxy holds their certificates. |
| `PAIL_S3_ENDPOINT` | none (required) | Where storage is: versitygw's URL, e.g. `http://versitygw:7070`, or any other store that speaks S3. |
| `PAIL_S3_ACCESS_KEY` · `PAIL_S3_SECRET_KEY` | none (required) | The store's keys. |
| `PAIL_S3_BUCKET` | `pail` | Bucket Pail keeps everything in; created if missing. |
| `PAIL_S3_REGION` | `us-east-1` | Region requests are signed for. |
| `PAIL_S3_ADDRESSING` | `auto` | Where the bucket's name goes in a request: `path` for after the host, `virtual` for in front of it, or `auto`, which is `virtual` for Amazon's, Google's and Alibaba's stores and `path` for every other. |

## API so far

Every call sends `Authorization: Bearer <PAIL_TOKEN>`. The API answers on the base domain, and on the server's IP or `localhost`. Errors are `{"error": {"code", "message"}}`.

| Method and path | What it does |
| --- | --- |
| `GET /api/v1/info` | Version, base domain and limits. |
| `GET /api/v1/pails` | Every pail, most recently updated first. |
| `GET /api/v1/pails/{name}` | One pail. |
| `POST /api/v1/pails/{name}/deploys` | Body is a `.tar.gz` or `.zip`. Creates the pail on its first deploy. Answers `202` with the deploy as soon as the archive arrives. `?source=cli\|upload`, `?file=<name>` for the history label. |
| `GET /api/v1/pails/{name}/deploys/{id}/log` | Server-sent events: a `line` event per log line, then one `done` event with the finished deploy. `?follow=false` sends a building deploy's lines so far and stops. |
| `GET /api/v1/pails/{name}/output` | Server-sent events: a `line` event for each line the pail's containers print, the kept ones first. `?follow=false` sends those and stops. |
| `GET /api/v1/pails/{name}/deploys` | The kept deploys, newest first, each with `serving: true\|false`. |
| `POST /api/v1/pails/{name}/serve` | Body `{"deploy": "<id>"}`. Points the pail at a kept deploy that finished; answers with the pail. `409` while a deploy is running or if that deploy failed. |
| `POST /api/v1/pails/{name}/redeploy` | Starts a deploy that copies the latest good one. Answers `202` like an upload; `409` if no deploy has finished. |
| `POST /api/v1/pails/{name}/stop` · `/start` | Turns the pail Off or back on; answers with the pail. |
| `GET /api/v1/pails/{name}/hosts` | The pail's addresses, its own first, each with `points_here` from a fresh DNS check. |
| `POST /api/v1/pails/{name}/hosts` | Body `{"host": "recipes.home.example"}`. Adds a custom hostname and checks it. `409` unless Let's Encrypt mode is on or `PAIL_TLS` is `off`, or if another pail has it. |
| `DELETE /api/v1/pails/{name}/hosts/{host}` | Removes a custom hostname. |
| `GET /api/v1/pails/{name}/env` | The pail's variables, by name. A secret comes with `secret: true` and no `value`. The pail needn't exist yet. |
| `PUT /api/v1/pails/{name}/env/{key}` | Body `{"value": "...", "secret": true}`. Sets a variable, replacing any by that name. `409` for a secret when this Pail has no key to seal it with. |
| `DELETE /api/v1/pails/{name}/env/{key}` | Removes a variable. |
| `GET /api/v1/check` | The DNS self-check for the base domain: do names under it reach this Pail? |
| `GET /api/v1/git` | The five git hosts, and which offer signing in. |
| `PUT /api/v1/git/{kind}` | Body `{"token": "...", "server": "..."}`. Checks the token with the host, then holds it for the next pail. Answers with the connection's `id`, never the token. `server` is for GitLab, Gitea and Forgejo. |
| `POST /api/v1/git/{kind}/oauth` | Begins signing in to a host that has an OAuth app set up. Answers with the address to send the browser to; the host sends it back to `/oauth/callback/{kind}`, which lands on New pail with the connection's id. With body `{"pail": "<name>"}` the sign-in is to reconnect that pail, and lands on the pail's page instead. |
| `GET /api/v1/git/{kind}/connections/{id}` | A connection that is waiting for its pail: whose it is, and how it was made. |
| `DELETE /api/v1/git/{kind}/connections/{id}` | Forgets a connection that is waiting for its pail. |
| `GET /api/v1/git/{kind}/repos?connection=` | The repos the connection can see. |
| `GET /api/v1/git/{kind}/detect?connection=&repo=&branch=` | What Pail makes of a repo, and whether it can deploy it. |
| `POST /api/v1/pails/{name}/repo` | Body `{"host", "connection", "repo", "branch"}`. Makes a new pail from a repo, deploys the branch, and adds a webhook so pushes deploy. The connection becomes the pail's own and can't be used again. |
| `PUT /api/v1/pails/{name}/connection` | Body `{"connection": "<id>"}`. Reconnects a pail from a git host: gives it a new connection, made like any other, in place of one that has run out or been revoked. Answers with whose it is. `409` if the pail isn't from a git host, or the connection is to another kind of host or can't see the pail's repo. |
| `POST /api/v1/hooks/{name}` | Where a git host delivers pushes. Takes no token: the delivery is signed with the hook's secret. |
| `DELETE /api/v1/pails/{name}` | Removes the pail and every deploy. |

## How a request is routed

- `<name>.<base domain>` is that pail's site, served from its live deploy. So is any custom hostname added to the pail.
- The base domain itself, an IP or `localhost` is the installation: the API under `/api/v1`, the web UI everywhere else.
- Any other host gets a plain 404 naming the installation.
- A pail that is Off answers every request with a plain 503 saying so.

On a pail's site, `/` and `/dir/` serve `index.html`, `/dir` redirects to `/dir/`, `/about` serves `about.html` if there is one, and a miss serves the deploy's `404.html` or, with a `fallback` in `pail.json`, that file.

## Git hosts

A pail can come from a repo on GitHub, GitLab, Bitbucket, Gitea or Forgejo. Each pail has a connection of its own: an access token or a sign-in, made on New pail for that pail and used for nothing else. Pail checks it with the host, holds it in memory for up to an hour while the pail is made, then stores it in the object store under `git/pails/`. Removing the pail removes it.

A pail whose token has expired or been revoked can't pull. **Reconnect**, on the pail's page, gives it a new connection in place of the old one, by token or by signing in; the pail, its deploys and its webhook stay as they are.

- **What Pail deploys.** A repo's files as they are: an `index.html` at the top, or a `pail.json` that says where the files live. A repo whose `package.json` has a build script is built first, where Pail can run microVMs (see Builds and microVMs), and declined where it can't.
- **More than one pail in a repo.** A pail can be one folder of a repo: give New pail the folder, like `apps/web`, or send `"dir": "apps/web"` when making the pail. That folder is then the pail's top: its `index.html`, `pail.json` and `package.json` are the ones Pail reads, and nothing outside it is deployed. Make one pail per folder, each with its own name.
- **Which pushes deploy a folder.** A pail that is a folder of its repo is redeployed by a push that changed a file in the folder, a `package.json`, lockfile, `pnpm-workspace.yaml` or `.npmrc` in a folder above it, or anything `watch` in its `pail.json` names, as paths from the top of the repo: `"watch": ["packages/ui"]`. Other pushes are answered and skipped. Pail deploys whenever the delivery doesn't list every file: Bitbucket never does, and no host does for a forced push, a new branch, or twenty commits or more. Only GitHub says a push was forced, so after rewriting a branch elsewhere, redeploy by hand.
- **Pushes deploy.** When a pail is made from a repo, Pail adds a webhook to it. Each push to the pail's branch is fetched and deployed like any other deploy. The host has to be able to reach Pail for this; a host on the internet can't reach a Pail that is only on your network.
- **If the webhook can't be added,** the pail is still made and says so. Redeploy pulls the branch by hand: `pail redeploy <pail>`.
- **Tokens.** For Bitbucket, an access token, or `email:api-token`. For the others, a personal access token that can read repos and add webhooks.
- **Signing in.** With an OAuth app set up for a host (`PAIL_OAUTH_<HOST>_CLIENT_ID` and `_CLIENT_SECRET`), New pail offers "Sign in with …" above the token form. The app's callback address is the one you open Pail at, followed by `/oauth/callback/<host>`. Pail renews a sign-in's token by itself when the host issues ones that run out. Signing in happens in the browser, so it works on a Pail only your network can reach.

The documentation site has a guide for each host under "Set up a git provider".

## Builds and microVMs

Pail runs builds in Firecracker microVMs, so a project's build can't touch the server. This needs Linux with KVM.

- **The KVM check.** At start Pail looks for `/dev/kvm`, `/dev/net/tun`, the Firecracker binary, the guest kernel, root, and the tools it prepares disks with (`mkfs.ext4`, `debugfs`, `ip`, `iptables`). The log says what it found, and `GET /api/v1/info` reports it under `builds`. Without them Pail serves static files as before, and a deploy that needs a build fails saying why.
- **What gets built.** A deploy whose `package.json` has a `build` script, from `pail up`, an upload or a git host. Pail installs dependencies with the package manager the lockfile names (npm, pnpm or yarn), runs the build, and serves what it leaves in `dist`, `build`, `out`, `_site`, `.output/public` or `public`, or the folder `static` names in `pail.json`. The source isn't stored or served. A build that leaves a `pail.json` of its own in that folder, as the Astro adapter in `adapters/astro` does, is deployed as that file describes: its files, and its functions.
- **Workspaces.** A project in a folder of a repo, from a git host or from `pail up`, is built with the whole repo in the microVM. Dependencies are installed where the nearest lockfile is, in the project's folder or one above it, and the build script runs in the project's folder. Only that one script runs: a package beside the project that has to be built first is the build script's to build.
- **The build VM.** A throwaway microVM from the `node:22-slim` image, with 2 vCPUs, 2GB of memory and 15 minutes. Its root filesystem is read-only and shared; everything it writes goes to a work disk that is deleted afterwards.
- **Isolation.** Each microVM has a network of its own. It can reach the internet through NAT for dependencies, and nothing else: not the home network, not other microVMs, not the server itself. The one exception is the containers of one pail, which reach each other (see Containers).
- **Guest init.** Inside a microVM, PID 1 is Pail's own binary, so there is nothing extra to install in an image.

Running microVMs needs Pail to run as root.

## Without KVM: Docker or Podman

Where there is no KVM, a laptop mostly, Pail runs the same things in the containers of a container engine instead. `internal/engine` implements what `internal/microvm` does, the `Machines` interface, over the engine's API socket: Docker's API, which Podman answers too, so there is one implementation and no command-line tool to install. `PAIL_RUNTIME` picks; left alone, Pail uses microVMs where it can and containers where it can't, and the log says which.

- **Siblings, not children.** Pail only ever talks to the socket and never shares a disk with the engine, so Pail can itself be one of the engine's containers. What it starts are its siblings. They join a network Pail is on too (`PAIL_CONTAINER_NETWORK`), and Pail reaches each at its address there. Files go in and out as tar archives over the API.
- **Builds.** A site builds in a throwaway container from `node:22-slim`, by the same script as in a microVM. A Dockerfile is built by the engine itself.
- **Containers.** A deploy keeps each container's image as one file, the archive an engine saves and loads, where a microVM's root filesystem would be. An image from a registry is fetched by Pail, not the engine, so the digest check that skips an unchanged image works the same. A data volume is one of the engine's own, named `<network>-vol.<pail>.<container>`; an empty file on Pail's disk stands for it, and when a removed pail's files go, the volume follows.
- **Functions.** A function's image is its language's image with the function's files and Pail's own binary added. A copy is a container of it, where that binary is the agent: it listens on the network, and speaks the protocol the agent in a microVM speaks over vsock. Each copy has a token of its own that requests must carry, since other containers on the network can reach it. There are no snapshots; a copy starts in a fraction of a second without one. What a deploy keeps is which image the function starts from, by digest, and its files.
- **A pail's containers find each other.** A deploy with more than one container gets a network of its own beside Pail's, `<network>-net.<pail>.<deploy>`, where each container answers to its name in `pail.json`. Another pail's containers aren't on it, so two pails can each have a `db`. The network goes when the last of its containers does.
- **Tidying.** Everything Pail makes carries the label `sh.pail.instance=<network>`. At start Pail removes the containers and networks an earlier run left, and a while later, the images nothing has asked for since.
- **What it isn't.** A container shares the machine's kernel, and containers on the network can reach each other and the home network. That is fine for your own code on your own machine. It is not the isolation a microVM gives, so it isn't for running code you don't trust.
- **Deploys don't move between the two.** What a deploy keeps for a microVM is not what it keeps for a container. A Pail switched from one runtime to the other serves its static files as before, and says of anything else to deploy it again.

`deploy/container/Dockerfile` builds the image: pail-server and versitygw, with `deploy/container/entrypoint.sh` running both and keeping everything under `/data`. Given a `PAIL_S3_ENDPOINT`, the entrypoint runs pail-server alone against that store, and `pail-container.sh` hands the `PAIL_S3_` settings through. `make image` builds it as `pail:dev`. `deploy/container/pail-container.sh` runs it, on Docker or Podman, and `make test-container` runs `scripts/container-smoke`, which does the same from this checkout and deploys one of everything to it.

## Containers

A `pail.json` at the top of a pail's upload can declare containers: Dockerfiles that Pail builds and keeps running, each in a Firecracker microVM of its own.

```json
{
  "static": "./public",
  "containers": {
    "api": { "dockerfile": "./Dockerfile", "port": 3000, "memory": "512MB", "data": "/data", "env": { "LOG_LEVEL": "info" } },
    "cache": { "image": "valkey/valkey:8", "port": 6379, "memory": "128MB" }
  },
  "routes": [
    { "path": "/api/*", "to": "container:api" },
    { "path": "/*", "to": "static", "fallback": "index.html" }
  ]
}
```

| Field | Default | What it sets |
| --- | --- | --- |
| `image` | none | An image in a registry to run as it is, like `nginx:1.27` or `ghcr.io/owner/app:latest`. A container has an `image` or a `dockerfile`, not both. |
| `dockerfile` | `./Dockerfile` | What to build, for a container with no `image`. |
| `context` | the Dockerfile's folder | The folder the build may `COPY` from. |
| `port` | none, required | The port the app listens on. It also arrives as `PORT`. The app has to listen on `0.0.0.0`. |
| `memory` | none, required | The microVM's memory, like `256MB`: the most the container can use. At most `PAIL_MAX_CONTAINER_MEMORY`. |
| `cpus` | `1` | The microVM's processors. |
| `data` | none | A folder kept on a volume that survives deploys, for SQLite and the like. |
| `command` | the image's `CMD` | A list of words to run in place of the image's `CMD`, like `["node", "server.js"]`. The image's `ENTRYPOINT` still goes in front, as with Docker. |
| `env` | none | Environment variables, on top of the image's own, `PORT`, `PAIL_NAME` and `PAIL_DEPLOY`. `${NAME}` in a value is one of the pail's variables. |

On each deploy Pail:

1. **Builds** the Dockerfile with Buildah inside a throwaway microVM (2 vCPUs, 2GB, 30 minutes), so a build can't touch the server. `FROM node:22` means Docker Hub's, as it does to Docker. A container with an `image` skips the build: Pail pulls the image for the server's architecture instead.
2. **Flattens** the image into an ext4 root filesystem and stores it with the deploy.
3. **Boots** it in a microVM of the size asked for, with the data volume attached. Pail's own binary is PID 1; it runs the image's `ENTRYPOINT` and `CMD` as the image's `USER`, in its `WORKDIR`.
4. **Waits** up to two minutes for the port to accept connections, then moves the live pointer and stops the old microVM. A deploy that never opens its port fails, and the previous one keeps serving. A deploy's containers are started together and each is waited for, so one may wait for another, as an app does for its database.

What follows from that:

- **Routes.** First match wins. `/api/*` covers `/api` and everything under it; a path with no star covers only itself. A path no route covers is a 404. With one container and nothing else, no routes are needed: it answers everything. Requests pass through with their method, body and `Host`, plus `X-Forwarded-For`, `-Host` and `-Proto`; websockets and event streams pass through too.
- **Files beside server code.** A deploy with containers or functions serves files only from the folder `static` names. Its source, Dockerfile included, is never served. A `package.json` build script is left to the Dockerfile.
- **Data.** A volume is 10GB, takes only the space it uses, and lives under `PAIL_DATA_DIR/volumes` on the server: back it up there. One microVM holds it at a time, so a container with `data` is stopped before its replacement starts, which is a few seconds of 503s per deploy. Without `data` the new microVM is answering before the old one stops. The volume goes when the pail is removed.
- **The root filesystem** is the container's own to write to, and is fresh on every start. Only `data` lasts.
- **Staying up.** If the app exits, Pail starts its microVM again, waiting a little longer each time it keeps happening. When Pail restarts, containers come back by themselves. `pail stop` shuts them down and `pail start` brings them back.
- **Images from a registry.** Each deploy asks the registry what the tag points at and keeps exactly that with the deploy, so a rollback runs what ran then even if the tag has moved. When the image hasn't changed since the deploy being served, nothing is fetched. `pail redeploy` asks again, which is how a pail picks up a moved tag such as `latest`. Pail unpacks an image's files on the server but never runs them there, and no link inside an image can make it write outside the image's own folder. There is no setting for registry credentials yet, so images have to be public.
- **Rollback** boots the root filesystem the older deploy was built with. Nothing is rebuilt.
- **Output.** stdout and stderr go to the deploy's log while it deploys, and to the pail's output after: `pail logs <pail> --output`, with `--follow` to keep reading, or `GET /api/v1/pails/{name}/output`. Pail keeps the last 2,000 lines, in memory.
- **Variables and secrets.** A pail has variables of its own, set with `pail env set` or on its page, for what a repo shouldn't hold. `${NAME}` in a container's or a function's `env` is filled in from them, `${NAME:-fallback}` has a fallback, and `$${NAME}` is left as `${NAME}`. A deploy whose `pail.json` uses one the pail lacks fails before anything is built. They are filled in each time a container or a function's copy starts, so a deploy keeps the reference and never the value, and a change is used from the next deploy. A secret is sealed with AES-256-GCM before it is stored (`meta/<pail>/env.json`), under a key that is never in the object store, and no API returns its value.
- **Each other.** A pail's containers reach each other by the names `pail.json` gives them: `api` finds the cache above at `cache:6379`. That is every port, not only the one `port` names, and only among the containers of one deploy: not another pail's, and not the deploy being replaced. Each container's address is kept for it while any of the deploy's containers is running, so one that restarts comes back where the others expect it. Functions aren't part of this.
- **Isolation.** As for builds: the internet through NAT, and nothing else but the pail's other containers. The only way in from outside is through Pail's router.
- **The home network, for pails you name.** `PAIL_ALLOW_LAN=media,backups` on the server lets those pails' containers reach private addresses: a NAS, another machine's API. It is the server's setting and not `pail.json`'s, so a repo can't grant it to itself; a name counts whether or not the pail exists yet, and a change takes a restart of Pail. Such a container still can't reach other pails' containers, or the services of the machine Pail runs on. Its connections arrive from that machine's address, since they go out through NAT. Names don't resolve through the home resolver, so use addresses. Builds and functions never get this. On Docker or Podman every container can reach the home network already, and the setting changes nothing.

`GET /api/v1/pails/{name}` lists the serving deploy's containers with their state: `running`, `starting` or `stopped`.

## Functions

A function is a program Pail runs once per request, with no Dockerfile and no server to write. `pail.json` says where its source is; Pail works out the language, builds it, and runs it in a microVM that sleeps when nothing is asking.

```json
{
  "static": "./build",
  "functions": {
    "api": { "src": "./fn/api" },
    "thumbs": { "src": "./fn/thumbs", "lang": "python", "timeout": "10s", "memory": "256MB", "idle": "15m", "max": 2, "env": { "MAX_SIZE": "512" } }
  },
  "routes": [
    { "path": "/api/*", "to": "function:api" },
    { "path": "/thumbs/*", "to": "function:thumbs" },
    { "path": "/*", "to": "static", "fallback": "index.html" }
  ]
}
```

| Field | Default | What it sets |
| --- | --- | --- |
| `src` | none, required | The function's source: a folder, or one file. |
| `lang` | detected | `python`, `node`, `ruby`, `go`, `rust` or `shell`. |
| `cmd` | the language's | What a request runs: a command line, or a list of words. |
| `timeout` | `10s` | How long one request may take, and how long it may wait for a copy. At most 15 minutes. |
| `memory` | `128MB` | Each copy's memory. At most `PAIL_MAX_FUNCTION_MEMORY`. |
| `idle` | `5m` | How long a copy waits for another request before it stops. |
| `max` | `4` | The most copies that run at once. |
| `env` | none | Environment variables. |

**A function is a handler.** Pail calls it with the request and a response to fill in:

```js
export default function (req, res) {
  res.status(200);
  res.contentType("application/json");
  return res.send(JSON.stringify({ path: req.path }));
}
```

`req` has `method`, `path`, `url`, `query`, `headers`, `body`, `text()`, `json()` and `form()`. `res` has `status`, `set`, `append`, `contentType` (`content_type` outside Node), `send`, `json` and `redirect`, each returning `res`. A handler may return its answer instead of sending it, what it prints goes to the pail's output, and one that throws is a 500.

- **Node, Python and Ruby.** The handler is the file's default export in Node, and a function named `handler` in Python and Ruby. Pail adds a small program of its own to the function's source, `.pail-handler.mjs`, `.py` or `.rb` from `internal/pails/handlers`, and runs the function's file with it. A file with no handler is run as a program that writes CGI, as before; what it writes while it loads is held until Pail knows which it is.
- **Go.** The standard library is enough: `cgi.Serve(http.HandlerFunc(handler))` from `net/http/cgi`.
- **Rust.** `sdk/rust` is the `pail-fn` crate, which depends on nothing: `pail_fn::handle(|req, res| { ... })`. A function depends on it with `pail-fn = "0.1"`.
- **Shell, and any function with a `cmd`**, writes CGI itself.

**Under the handler, the interface is CGI** (RFC 3875). The request arrives as `REQUEST_METHOD`, `PATH_INFO` (the full request path), `QUERY_STRING`, `CONTENT_TYPE`, `CONTENT_LENGTH`, each header as `HTTP_<NAME>`, `PAIL_NAME` and `PAIL_DEPLOY`, with the body on stdin. The program writes header lines, a blank line, then the body. `Status: 404` sets the status; without one it is 200, or 302 when there is a `Location`. With no `Content-Type` it is `text/plain`. stderr goes to the pail's output.

| lang | Detected by | Built with | Runs |
| --- | --- | --- | --- |
| python | `requirements.txt` or `main.py` | `pip install -r requirements.txt` | `main.py` |
| node | `package.json` or `index.js` | `npm ci`, or `npm install` without a lockfile | `main` in `package.json`, else `index.js` |
| ruby | `Gemfile` or `main.rb` | `bundle install` | `main.rb`, through `bundle exec` with a Gemfile |
| go | `go.mod` | `go build -o fn` | `./fn` |
| rust | `Cargo.toml` | `cargo build --release` | the built binary |
| shell | `main.sh` | nothing | `sh main.sh` |

A `src` that is one file is told apart by its extension and run directly.

On each deploy Pail:

1. **Builds** the source, where the language has a build step, in a throwaway microVM from the language's image, with the internet for dependencies and nothing else.
2. **Snapshots.** It boots the function's microVM once, runs the language's interpreter so it is already in memory, and saves a Firecracker snapshot. The root filesystem, the built source and the snapshot are stored with the deploy, so a rollback restores exactly what ran then.
3. **Runs on request.** The first request restores the snapshot. An agent inside (Pail's own binary, as PID 1) takes each request from Pail over vsock, runs the program as a fresh process, and sends back its output.
4. **Sleeps.** A copy with no requests for `idle` is stopped. The next request restores the snapshot again.

What follows from that:

- **Copies.** A copy handles one request at a time. A request goes to an idle copy; if every copy is busy and fewer than `max` are running, Pail restores another; if `max` are busy, the request waits, and one still waiting when its `timeout` runs out gets a 503, with a line in the pail's output saying the function was at its max.
- **Failures.** A non-zero exit, running out of memory, a timeout, more than 32MB of output, or output that doesn't start with header lines returns a 500. The response and the pail's output both say which.
- **Disk.** A function's files and root filesystem are read-only and shared by its copies. `/tmp` is in memory: what one request leaves there the next may find, on the same copy, until it sleeps. Use a container's `data` for anything that has to last.
- **Responses are whole.** Pail holds a program's output until it exits, to know whether it failed. Streaming needs a container.
- **Network.** As for builds: the internet through NAT, and nothing else.
- **Where a snapshot can't be taken or restored,** the function still runs, booting each copy from cold, and the deploy's log says so.
- **Memory with containers.** `memory` is required for a container and optional for a function.

`GET /api/v1/pails/{name}` lists the serving deploy's functions with how many copies of each are awake, and its routes.

### Developing this on a Mac

Firecracker doesn't run on macOS, so it runs on a KVM host: a Lima VM on your Mac (Apple M3 or later, for nested virtualization), or any Linux machine over SSH. `KVM_HOST` says which.

| Command | What it does |
| --- | --- |
| `make kvm-up` | Starts the Lima VM, or checks the SSH host, and installs Firecracker and a guest kernel there. |
| `make kvm-check` · `make kvm-smoke` | Says whether the host is ready; boots one microVM to prove it. |
| `make dev-kvm` | Runs Pail on the KVM host, with builds on, at http://localhost:8080. |
| `make test-kvm` | Runs the tests that boot real microVMs. |
| `make kvm-shell` · `make kvm-down` | A shell on the host; stops the Lima VM. |

`KVM_HOST=local` uses this machine itself, when it is Linux with KVM. CI does.

For a Linux machine instead of Lima, as a user who can `sudo` without a password:

```bash
make kvm-up KVM_HOST=chris@homelab
```

## Hostnames and the DNS check

Pail routes by the Host header, so every pail name and custom hostname has to resolve to this server. Pail checks that by asking for a one-time path, `/.well-known/pail/<nonce>`, at the hostname and seeing whether the request comes back to itself. It never needs to know its own outside address, so the check works the same behind Docker port mapping, in an LXC or in a VM.

- **On start**, Pail checks a random name under the base domain and logs whether it arrived.
- **In the UI**, Your pails shows a note when names under the base domain don't reach Pail, and a pail's Addresses show "Points here" or "Not pointing here yet" for each hostname.
- **`pail hosts`** reports the same.

A hostname is added whether or not its DNS is ready; it starts answering as soon as it points here. Custom hostnames need Let's Encrypt mode, or a proxy that handles HTTPS in front of Pail (see TLS).

## TLS

Pail serves every pail over HTTPS from its own listener. The plain listener answers only the DNS self-check and `/ca.crt`, and redirects everything else to HTTPS. There are two ways Pail gets certificates.

**Pail's own authority (the default).** On first start Pail makes a root certificate, valid ten years, that can only sign for the base domain: it carries a critical name constraint, so even its key can't be used to impersonate another site. From it Pail issues a wildcard for the base domain that lasts a week and is replaced at half-life; nobody handles those. Each device trusts the root once: it's at `http://<base domain>/ca.crt`, `pail ca` prints it, and the web UI's "Trust this Pail" page has the steps per platform. This mode has no custom hostnames.

**Let's Encrypt (when `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` are set).** The base domain must be a real one you control. Pail proves ownership by having the DNS provider publish a TXT record, so it works on a server with private addresses and no open ports. It gets a wildcard for the base domain, and a certificate of its own for each custom hostname, and renews them when a third of their life is left.

- **First start waits.** Pail doesn't begin serving until it has the wildcard, and refuses to start, saying why, if it can't get one.
- **Adding a hostname waits too.** `pail hosts add` and the UI return once the hostname's certificate is issued. If Let's Encrypt won't issue one, because the hostname isn't in a zone the token can edit, the hostname isn't added.
- **Try it on staging first.** Let's Encrypt's production directory has rate limits. Set `PAIL_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory` until it works, then remove it.

**Behind a proxy (`PAIL_TLS=off`).** Pail serves plain HTTP on `PAIL_LISTEN` and gets no certificates; whatever is in front of it, a reverse proxy or a Cloudflare Tunnel, holds them and answers HTTPS. Custom hostnames are allowed, and are added without a certificate: the proxy has to answer for each one and pass it on with its `Host` unchanged. Pail reads `X-Forwarded-Proto` to tell whether the visitor came over HTTPS.

The base domain needs at least two labels (`pail.lan`, not `localhost`): browsers refuse a wildcard certificate directly under a single-label name. `make dev` runs with `PAIL_TLS=off` for that reason.

The root's key, the ACME account and the certificates are kept in the object store under `tls/`, beside everything else Pail stores.

## Storage layout

```
pails/<name>/deploys/<id>/...             the unpacked upload, never changed
meta/<name>/state.json                    the pail and its live pointer
meta/<name>/deploys/<id>.json             the deploy record
meta/<name>/deploys/<id>.log              its log
meta/<name>/deploys/<id>.manifest.json    what it serves
git/pails/<name>.json                     the pail's connection to its git host: server and access token
git/<host>.json                           a connection from when all of a host's pails shared one; pails made then still use it
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

## Releases

Pushing a tag like `v0.1.0` to `chrisdmacrae/pail` runs `.github/workflows/release.yml`, which tests, builds and publishes a GitHub release with:

| File | What it is |
| --- | --- |
| `pail-server_linux_<arch>.tar.gz` | The server, with the web UI inside, for amd64 and arm64. |
| `pail_<os>_<arch>.tar.gz` or `.zip` | pail-cli for macOS, Linux and Windows, amd64 and arm64. |
| `pail-proxmox.sh` | The Proxmox installer. |
| `pail-container.sh` | Runs Pail in a container, on Docker or Podman. |
| `checksums.txt` | SHA-256 of each file. |

It also builds the image `pail-container.sh` runs, for amd64 and arm64, and pushes it to `ghcr.io/chrisdmacrae/pail` as the version and, unless the version is a preview, as `latest`.

The release's description opens with how to install that version: the server on Proxmox or in a container, and pail-cli with Homebrew. `scripts/release-notes <version>` prints that part, and GitHub's list of what changed follows it.

File names carry no version, so `releases/latest/download/<name>` always points at the newest. `make release VERSION=v0.1.0` builds the same files into `dist/release` on your machine.

### The Homebrew tap

`brew install chrisdmacrae/tap/pail` reads `Formula/pail.rb` in [chrisdmacrae/homebrew-tap](https://github.com/chrisdmacrae/homebrew-tap). The formula installs the binary the release built, for macOS and Linux on amd64 and arm64, so brew compiles nothing.

The release workflow rewrites the formula at every release: `scripts/brew-formula <version>` prints it from `checksums.txt`, and the workflow commits it to the tap. A version with a dash in it, like `v0.2.0-rc1`, is a preview and leaves the tap alone.

Pushing to the tap needs a key: the `HOMEBREW_TAP_KEY` secret on this repo holds the private half of a deploy key on the tap that can write. Without the secret the release is still published, and the tap stays as it was. To update the tap by hand:

```bash
scripts/brew-formula v0.1.0 dist/release/checksums.txt > ../homebrew-tap/Formula/pail.rb
```

The Rust crate, `pail-fn` in `sdk/rust`, is released on its own, with no tag. A push to `main` that changes its `src` folder or its `Cargo.toml` runs `.github/workflows/release-rust.yml`, which tests it and publishes it to crates.io. `scripts/crate-next-version sdk/rust` picks the version: the one in `Cargo.toml` the first time, then the next patch each time after. To start a new minor or major, change `Cargo.toml`. Publishing needs the `CARGO_REGISTRY_TOKEN` secret on this repo: a crates.io API token that may publish new crates, for the first release, and update `pail-fn` after it.

`.github/workflows/ci.yml` runs `make check` on every push and pull request, builds a release without publishing it, runs the microVM tests on GitHub's runners, which have KVM, and runs `scripts/container-smoke` against the runners' Docker.

## Running on Proxmox

`deploy/proxmox/pail-lxc.sh` sets Pail up in an unprivileged LXC container on a Proxmox host: versitygw and pail-server as services, Firecracker and a guest kernel, and `/dev/kvm` and `/dev/net/tun` passed through for builds. Pail's settings are one file in the container, `/etc/pail/pail.env`, which its service reads as it starts. With `S3_ENDPOINT`, `S3_ACCESS_KEY` and `S3_SECRET_KEY` the installer points Pail at that store and leaves versitygw out. On the host, as root:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
```

It needs a published release to download the server from. The documentation site's "Running on Proxmox" page covers its settings and looking after the container.

## Running on Docker or Podman

`deploy/container/pail-container.sh` runs Pail on the machine you're at, in one container that holds versitygw and pail-server, with the engine's socket mounted so that builds, containers and functions run as containers beside it. With Docker or Podman running:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

Pail answers at http://localhost:8080, with `localhost` as the base domain, so `<name>.localhost:8080` needs no DNS. Running it again updates Pail and keeps its data; `down` as its argument stops and removes it. It needs a published release for the image. To run this checkout instead:

```bash
make image && PAIL_IMAGE=pail PAIL_VERSION=dev bash deploy/container/pail-container.sh
```

The documentation site's "Running on Docker" and "Running on Podman" pages cover its settings.

## Documentation site

`docs/` is a static site built with Astro and styled from `web/design-system/`. Every page is a Markdown file in `docs/src/content/docs`, an Astro content collection: a file's path is its URL, and its front matter (title, lead, section, order) puts it in the sidebar. Adding a page is adding a file.

```bash
make dev-docs
```
