# Contributing to Pail

How to run Pail from a checkout, test it, and release it, with notes on how it works inside. For running Pail to use it, see the [README](README.md) and the [documentation](https://pail.chrisdmacrae.com).

## Run it from a checkout

You need Go, Node with pnpm, and `make`.

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

Development serves plain HTTP: no wildcard certificate can cover names directly under `localhost`, so `make dev` runs with `PAIL_TLS=off`. To try HTTPS, use a base domain with a dot in it: `make dev-server PAIL_TLS=on PAIL_BASE_DOMAIN=pail.test`.

The web UI in `web/ui` is built from `web/design-system/` as published: its components come from that folder's `bundle.js`, and nothing in the folder is edited.

## Test

```bash
make check
```

That is lint, every test, and a build of the documentation site. Run it before a commit.

## MicroVMs, on a Mac

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

## Docker or Podman in place of microVMs

Where there is no KVM, a laptop mostly, Pail runs the same things in the containers of a container engine instead. `internal/engine` implements what `internal/microvm` does, the `Machines` interface, over the engine's API socket: Docker's API, which Podman answers too, so there is one implementation and no command-line tool to install. `PAIL_RUNTIME` picks; left alone, Pail uses microVMs where it can and containers where it can't, and the log says which.

`deploy/container/Dockerfile` builds the image: pail-server and versitygw, with `deploy/container/entrypoint.sh` running both and keeping everything under `/data`. Given a `PAIL_S3_ENDPOINT`, the entrypoint runs pail-server alone against that store, and `pail-container.sh` hands the `PAIL_S3_` settings through. `make image` builds it as `pail:dev`. `deploy/container/pail-container.sh` runs it, on Docker or Podman, and `make test-container` runs `scripts/container-smoke`, which does the same from this checkout and deploys one of everything to it.

- **Siblings, not children.** Pail only ever talks to the socket and never shares a disk with the engine, so Pail can itself be one of the engine's containers. What it starts are its siblings. They join a network Pail is on too (`PAIL_CONTAINER_NETWORK`), and Pail reaches each at its address there. Files go in and out as tar archives over the API.
- **Builds.** A site builds in a throwaway container from `node:22-slim`, by the same script as in a microVM. A Dockerfile is built by the engine itself.
- **Containers.** A deploy keeps each container's image as one file, the archive an engine saves and loads, where a microVM's root filesystem would be. An image from a registry is fetched by Pail, not the engine, so the digest check that skips an unchanged image works the same. A data volume is one of the engine's own, named `<network>-vol.<pail>.<container>`; an empty file on Pail's disk stands for it, and when a removed pail's files go, the volume follows.
- **Functions.** A function's image is its language's image with the function's files and Pail's own binary added. A copy is a container of it, where that binary is the agent: it listens on the network, and speaks the protocol the agent in a microVM speaks over vsock. Each copy has a token of its own that requests must carry, since other containers on the network can reach it. There are no snapshots; a copy starts in a fraction of a second without one. What a deploy keeps is which image the function starts from, by digest, and its files.
- **A pail's containers find each other.** A deploy with more than one container gets a network of its own beside Pail's, `<network>-net.<pail>.<deploy>`, where each container answers to its name in `pail.json`. Another pail's containers aren't on it, so two pails can each have a `db`. The network goes when the last of its containers does.
- **Tidying.** Everything Pail makes carries the label `sh.pail.instance=<network>`. At start Pail removes the containers and networks an earlier run left, and a while later, the images nothing has asked for since.
- **What it isn't.** A container shares the machine's kernel, and containers on the network can reach each other and the home network. That is fine for your own code on your own machine. It is not the isolation a microVM gives, so it isn't for running code you don't trust.
- **Deploys don't move between the two.** What a deploy keeps for a microVM is not what it keeps for a container. A Pail switched from one runtime to the other serves its static files as before, and says of anything else to deploy it again.

## The documentation site

`docs/` is a static site built with Astro and styled from `web/design-system/`. Every page is a Markdown file in `docs/src/content/docs`, an Astro content collection: a file's path is its URL, and its front matter (title, lead, section, order) puts it in the sidebar. Adding a page is adding a file.

```bash
make dev-docs
```

## How Pail works inside

### How a request is routed

- `<name>.<base domain>` is that pail's site, served from its live deploy. So is any custom hostname added to the pail.
- The base domain itself, an IP or `localhost` is the installation: the API under `/api/v1`, the web UI everywhere else.
- Any other host gets a plain 404 naming the installation.
- A pail that is Off answers every request with a plain 503 saying so.

On a pail's site, `/` and `/dir/` serve `index.html`, `/dir` redirects to `/dir/`, `/about` serves `about.html` if there is one, and a miss serves the deploy's `404.html` or, with a `fallback` in `pail.json`, that file.

### The DNS check

Pail routes by the Host header, so every pail name and custom hostname has to resolve to this server. Pail checks that by asking for a one-time path, `/.well-known/pail/<nonce>`, at the hostname and seeing whether the request comes back to itself. It never needs to know its own outside address, so the check works the same behind Docker port mapping, in an LXC or in a VM.

### The API

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
| `POST /api/v1/git/{kind}/oauth` | Begins signing in to a host that has an OAuth app set up. Answers with the address to send the browser to; the host sends it back to `/oauth/callback/{kind}`, which lands on New pail with the connection's id. With body `{"pail": "<name>"}` the sign-in is to reconnect that pail, and lands on the pail's page instead. With `"repo": true` as well, it is to give that pail a repo, and lands on `/pails/<name>/git`, where one is picked. |
| `GET /api/v1/git/{kind}/connections/{id}` | A connection that is waiting for its pail: whose it is, and how it was made. |
| `DELETE /api/v1/git/{kind}/connections/{id}` | Forgets a connection that is waiting for its pail. |
| `GET /api/v1/git/{kind}/repos?connection=` | The repos the connection can see. |
| `GET /api/v1/git/{kind}/detect?connection=&repo=&branch=` | What Pail makes of a repo, and whether it can deploy it. |
| `POST /api/v1/pails/{name}/repo` | Body `{"host", "connection", "repo", "branch"}`. Makes a new pail from a repo, deploys the branch, and adds a webhook so pushes deploy. The connection becomes the pail's own and can't be used again. |
| `PUT /api/v1/pails/{name}/repo` | The same body. Has a pail there already is deploy from the repo from here on: one from `pail up` or an upload, or one that deploys from another repo, whose webhook is taken off. The pail keeps its name, hostnames, variables and deploys. `404` if there is no such pail. |
| `DELETE /api/v1/pails/{name}/repo` | Has a pail stop deploying from its repo: takes the webhook off, forgets the pail's connection, and answers with the pail. It keeps serving and keeps its deploys, and is deployed with `pail up` or an upload from then on. `409` if the pail isn't from a git host. |
| `PUT /api/v1/pails/{name}/connection` | Body `{"connection": "<id>"}`. Reconnects a pail from a git host: gives it a new connection, made like any other, in place of one that has run out or been revoked. Answers with whose it is. `409` if the pail isn't from a git host, or the connection is to another kind of host or can't see the pail's repo. |
| `POST /api/v1/hooks/{name}` | Where a git host delivers pushes. Takes no token: the delivery is signed with the hook's secret. |
| `DELETE /api/v1/pails/{name}` | Removes the pail and every deploy. |

### Storage layout

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

The Astro adapter, `astro-pail` in `adapters/astro`, is released on its own, with no tag. A push to `main` that changes its `src` folder or its `package.json` runs `.github/workflows/release-astro.yml`, which publishes it to npm. `scripts/npm-next-version` picks the version: the one in `package.json` the first time, then the next patch each time after.

The Rust crate, `pail-fn` in `sdk/rust`, is released on its own, with no tag. A push to `main` that changes its `src` folder or its `Cargo.toml` runs `.github/workflows/release-rust.yml`, which tests it and publishes it to crates.io. `scripts/crate-next-version sdk/rust` picks the version: the one in `Cargo.toml` the first time, then the next patch each time after. To start a new minor or major, change `Cargo.toml`. Publishing needs the `CARGO_REGISTRY_TOKEN` secret on this repo: a crates.io API token that may publish new crates, for the first release, and update `pail-fn` after it.

`.github/workflows/ci.yml` runs `make check` on every push and pull request, builds a release without publishing it, runs the microVM tests on GitHub's runners, which have KVM, and runs `scripts/container-smoke` against the runners' Docker.
