---
title: What Pail deploys
lead: "Hand Pail a folder, a zip or a repo. It works out what’s inside and puts it live."
section: Deploying
order: 3
next:
  href: /deploying/functions/
  label: Write a function
---

## Four kinds of project

Pail looks at what you send and picks how to serve it. You don’t choose; it guesses, and the deploy’s log says what it found.

| What’s in the upload | What Pail does |
| --- | --- |
| An `index.html` at the top | Serves the files as they are. |
| A `package.json` with a `build` script | Builds the project first, then serves what the build made. |
| A `pail.json` that declares functions | Builds each one and runs it when a request arrives. See [Write a function](/deploying/functions/). |
| A `pail.json` that declares containers | Builds each Dockerfile, or pulls each image, and keeps it running. See [Run a container](/deploying/containers/). |

Every way in works the same: `pail up` from a terminal, a folder dropped on New pail, or a push to [a connected repo](/git-providers/).

## Files, as they are

A folder with an `index.html` at the top needs nothing else.

```bash
pail up ./dist
```

`/` and `/blog/` serve `index.html`, `/about` serves `about.html` if there is one, and a path with no file serves the deploy’s `404.html`.

## Projects that need a build

When the upload has a `package.json` with a `build` script, Pail builds it on every deploy. It installs dependencies with the package manager your lockfile names (npm, pnpm or yarn), runs the build, and serves what it leaves in `dist`, `build`, `out`, `_site`, `.output/public` or `public`.

The build runs in a small virtual machine that is thrown away afterwards. It can reach the internet for dependencies and nothing on your network. Your source is not stored or served, only the result.

A build can make functions too. When it leaves a `pail.json` in its output folder, Pail deploys what that file describes: the files it points `static` at, and the functions it declares. This is how [an Astro site](/deploying/astro/) gets pages rendered on demand.

Builds need `/dev/kvm` on the server, or a Pail on [Docker](/running/docker/) or [Podman](/running/podman/), which builds in a container instead. Where there is neither, Pail says so, and you can still build on your own machine and send the result with `pail up ./dist`.

## pail.json

A `pail.json` at the top of the upload tells Pail what it can’t guess. Every field is optional.

```json
{
  "name": "garden",
  "static": "./public",
  "routes": [{ "path": "/*", "to": "static", "fallback": "index.html" }]
}
```

| Field | What it sets |
| --- | --- |
| `name` | The pail’s name, when `pail up` isn’t given one with `--name`. |
| `static` | The folder that holds the files to serve. With a build, the folder the build leaves them in. |
| `watch` | For one pail of a repo that holds several: folders and files elsewhere in the repo that a push redeploys this pail for. See [More than one pail in a repo](/git-providers/#more-than-one-pail-in-a-repo). |
| `routes[].fallback` | A file served when a path matches nothing, for single-page apps that handle their own routes. |
| `functions` | Programs Pail runs once per request. See [Write a function](/deploying/functions/). |
| `containers` | Dockerfiles to build, or images to pull, and run. See [Run a container](/deploying/containers/). |
| `routes` | Which paths go to files, a function or a container. First match wins. |

## Every deploy is kept

A deploy never changes once it’s made, and going live is one switch: a visitor gets the old deploy or the new one, never a mix. If a deploy fails, the one before it keeps serving.

Pail keeps the last ten good deploys of each pail. Any of them can be served again from the pail’s page, or with:

```bash
pail rollback garden 7e53dec
```

Nothing is rebuilt. Pail serves what that deploy was made of.
