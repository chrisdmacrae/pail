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
| 2 | pail-cli with profiles: login, up, ls, logs | next |
| 3 | Deploy history and rollback | |
| 4 | Web UI from the design system | |
| 5 | Custom hostnames and the DNS self-check | |
| 6 | Git hosts: token first, then OAuth | |
| 7 | Firecracker microVMs: the build VM, containers, and the KVM check | |
| 8 | pail.json functions: base images, snapshots, sleep when idle, and routing | |

## Layout

```
cmd/pail-server      the server binary
internal/config      environment variables
internal/storage     the object store: S3 (versitygw) and an in-memory one for tests
internal/pails       pails, deploys, the live pointer, the deploy pipeline
internal/server      the listener: Host routing, the REST API, static serving
web/design-system    tokens, components, fonts and brand marks, as published
web/prototype        the prototype's source, for matching screens and copy in step 4
```

## Run it

Pail needs versitygw (or any S3 gateway) to keep pails in. For a local one:

```bash
ROOT_ACCESS_KEY=pail ROOT_SECRET_KEY=pail-dev-secret versitygw --port 127.0.0.1:7070 posix ./data
```

Then, with `localhost` as the base domain so `<name>.localhost` resolves without DNS:

```bash
PAIL_TOKEN=dev-token PAIL_BASE_DOMAIN=localhost PAIL_LISTEN=127.0.0.1:8080 \
PAIL_S3_ENDPOINT=http://127.0.0.1:7070 PAIL_S3_ACCESS_KEY=pail PAIL_S3_SECRET_KEY=pail-dev-secret \
go run ./cmd/pail-server
```

Deploy a folder and open it:

```bash
tar -czf site.tgz -C ./dist .
curl -H "Authorization: Bearer dev-token" --data-binary @site.tgz "http://localhost:8080/api/v1/pails/blog/deploys?source=cli"
open http://blog.localhost:8080
```

## Configuration

Everything is an environment variable on the server.

| Variable | Default | What it sets |
| --- | --- | --- |
| `PAIL_TOKEN` | none (required) | The installation's secret access token. Pail refuses to start without it. |
| `PAIL_BASE_DOMAIN` | `pail.lan` | The domain every pail gets a name under. |
| `PAIL_MAX_UPLOAD_SIZE` | `100MB` | Largest archive accepted; bigger ones get a 413. |
| `PAIL_MAX_DEPLOYS` | `10` | Deploys kept per pail. |
| `PAIL_MAX_FUNCTION_MEMORY` | `1GB` | Reported by `/api/v1/info`; not enforced until functions exist. |
| `PAIL_LISTEN` | `:80` | Address the listener binds. |
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
| `GET /api/v1/pails/{name}/deploys/{id}/log` | Server-sent events: a `line` event per log line, then one `done` event with the finished deploy. |
| `DELETE /api/v1/pails/{name}` | Removes the pail and every deploy. |

## How a request is routed

- `<name>.<base domain>` is that pail's site, served from its live deploy.
- The base domain itself, an IP or `localhost` is the installation: the API.
- Any other host gets a plain 404 naming the installation.

On a pail's site, `/` and `/dir/` serve `index.html`, `/dir` redirects to `/dir/`, `/about` serves `about.html` if there is one, and a miss serves the deploy's `404.html` or, with a `fallback` in `pail.json`, that file.

## Storage layout

```
pails/<name>/deploys/<id>/...             the unpacked upload, never changed
meta/<name>/state.json                    the pail and its live pointer
meta/<name>/deploys/<id>.json             the deploy record
meta/<name>/deploys/<id>.log              its log
meta/<name>/deploys/<id>.manifest.json    what it serves
```

Going live is one write of `state.json`. If anything fails before it, the pointer never moves.

## Test

```bash
go test -race ./...
```
