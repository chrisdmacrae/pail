---
title: Run a container
lead: "Give Pail a Dockerfile, or the name of an image, and a port. It keeps the container running and sends requests to it."
section: Deploying
order: 5
navLabel: Run a container
next:
  href: /git-providers/
  label: Set up a git provider
---

## When to use one

Use a container when the thing you’re hosting is a server that stays up: an app with a database, anything that needs websockets, background work, or data that lasts. For a small program that only answers requests, [a function](/deploying/functions/) is less to write and costs nothing while idle.

Each container runs in a small virtual machine of its own. It can reach the internet, and nothing on your network. The only way in is through Pail.

Containers need `/dev/kvm` on the server. [Running Pail](/running/) covers that.

## 1. Add a pail.json

Put a `pail.json` beside your Dockerfile, with the port your app listens on and how much memory it may use.

```json
{
  "containers": {
    "web": { "port": 3000, "memory": "256MB" }
  }
}
```

That is enough. With one container and nothing else, every request goes to it.

`memory` is required. It is a hard limit: the container gets that much and no more. The server sets the most one container may ask for, 2GB unless `PAIL_MAX_CONTAINER_MEMORY` says otherwise.

Your app has to listen on `0.0.0.0`, not `127.0.0.1`. The port also arrives in the `PORT` environment variable, if you’d rather read it than repeat it.

## 2. Deploy it

From the folder that holds them:

```bash
pail up
```

Pail builds the Dockerfile, boots the image, and waits for the port to answer before it switches over. The log shows each step. If the build fails or the port never opens, the deploy fails and the last one keeps serving.

A push to [a connected repo](/git-providers/) does the same.

## Run an image from a registry

If the image already exists, name it and skip the Dockerfile. The upload can be the `pail.json` and nothing else.

```json
{
  "containers": {
    "web": { "image": "nginx:1.27", "port": 80, "memory": "128MB" }
  }
}
```

A short name like `nginx:1.27` means Docker Hub’s. For another registry, write it in full: `ghcr.io/owner/app:latest`. The image has to be public, and built for the server’s kind of processor.

Each deploy asks the registry what the tag points at and keeps exactly that. So a rollback runs what ran then, even if the tag has moved since. To pick up a moved tag such as `latest`, deploy again:

```bash
pail redeploy web
```

## Settings

Each container takes these in `pail.json`. `port` and `memory` are required.

| Field | Default | What it sets |
| --- | --- | --- |
| `port` | none | The port the app listens on. |
| `memory` | none | The most memory it can use, like `256MB`. |
| `image` | none | An image to run as it is. Use this or `dockerfile`, not both. |
| `dockerfile` | `./Dockerfile` | What to build, when there is no `image`. |
| `context` | the Dockerfile’s folder | The folder the build can `COPY` from. |
| `command` | the image’s own | What to run, as a list of words. See below. |
| `cpus` | `1` | How many processors it gets. |
| `data` | none | A folder that keeps its contents between deploys. |
| `env` | none | Environment variables for the app. |

## Change what it runs

An image says what to run when it starts. To run something else, or pass it different arguments, give a `command` as a list of words:

```json
{
  "containers": {
    "cache": { "image": "valkey/valkey:8", "port": 6379, "memory": "128MB", "command": ["valkey-server", "--save", "60", "1"] }
  }
}
```

`command` takes the place of the image’s `CMD`. If the image has an `ENTRYPOINT`, that still runs first with your command as its arguments, the same as with Docker.

## Keep data between deploys

A container starts from a fresh copy of its image every time. To keep something, such as a SQLite database, name a folder for it:

```json
{
  "containers": {
    "web": { "port": 3000, "memory": "256MB", "data": "/data" }
  }
}
```

Whatever the app writes under `/data` is still there after a deploy, a restart or a rollback. It is deleted when the pail is removed.

The data lives on the server’s own disk, under `volumes` in Pail’s data folder (`/var/lib/pail` unless you changed it). Back it up there.

One thing to know: a container with `data` is stopped a few seconds before its replacement starts, because only one of them can hold the data at a time. Visitors see a short pause on each deploy. Without `data`, the new one is answering before the old one stops.

## Files and a container together

To serve a built front end beside an API, say which folder holds the files and which paths go where:

```json
{
  "static": "./public",
  "containers": {
    "api": { "port": 3000, "memory": "256MB", "data": "/data" }
  },
  "routes": [
    { "path": "/api/*", "to": "container:api" },
    { "path": "/*", "to": "static", "fallback": "index.html" }
  ]
}
```

Routes are read top to bottom and the first match wins. `/api/*` covers `/api` and everything under it. A path with no star covers only itself. A path that no route covers is not found.

Only the `static` folder is served as files. The rest of your project, Dockerfile included, is never served.

## See what it’s printing

Whatever the app writes to stdout or stderr shows on the pail’s page, under Output. From a terminal:

```bash
pail logs notes --output --follow
```

Pail keeps the last 2,000 lines.

## What Pail does for you

- **Restarts it.** If the app exits, Pail starts it again. If it keeps exiting, Pail waits a little longer each time.
- **Brings it back.** When the server restarts, containers come back up by themselves.
- **Stops it when asked.** `pail stop notes` shuts the container down; `pail start notes` brings it back.
- **Rolls it back.** Serving an older deploy boots the image that deploy was built with. Nothing is rebuilt.

## When a deploy fails

| The log says | What to do |
| --- | --- |
| It didn’t open its port | Check the app listens on `0.0.0.0` and on the port `pail.json` names. |
| It stopped before it opened its port | The app exited. Its own output, just above in the log, says why. |
| It didn’t build | The Dockerfile’s build failed. The step that failed is the last one in the log. |
| It has no CMD or ENTRYPOINT | Add one to the Dockerfile, or say what to run with `command`. |
| Its image didn’t arrive | Check the image’s name and tag, and that it is public. |
| It asks for more memory than this Pail gives | Ask for less, or raise `PAIL_MAX_CONTAINER_MEMORY` on the server. |
| This Pail can’t run containers | The server has no `/dev/kvm`. See [Running Pail](/running/). |
