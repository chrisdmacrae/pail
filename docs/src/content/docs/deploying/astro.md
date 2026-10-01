---
title: Deploy an Astro site
lead: "Add Pail’s adapter to an Astro project. Prerendered pages are served as files, and pages rendered on demand and API routes are answered by a function."
section: Deploying
order: 7
navLabel: Deploy an Astro site
next:
  href: /git-providers/
  label: Set up a git provider
---

## When you need it

An Astro site where every page is prerendered needs nothing: Pail builds it and serves the files.

Add the adapter when some pages are rendered on demand, or the site has API routes, actions or server islands. The adapter turns those into one [function](/deploying/functions/), and leaves the rest as files.

These docs are built this way. Every page is a file, and two API routes are answered by the function: the search box asks one, and the other draws the picture a link to a page unfurls with.

## 1. Add the adapter

```bash
npm install astro-pail
```

In `astro.config.mjs`:

```js
import { defineConfig } from 'astro/config';
import pail from 'astro-pail';

export default defineConfig({
  adapter: pail(),
});
```

## 2. Say which pages are rendered on demand

Astro prerenders every page unless told otherwise. A page or an API route opts out with one line:

```js
export const prerender = false;
```

To render on demand by default, set `output: 'server'` in the config, and mark the pages to prerender with `export const prerender = true`.

## 3. Deploy it

```bash
pail up
```

Pail builds the project, and the build leaves a `pail.json` in `dist` that says which paths are files and which go to the function. A push to [a connected repo](/git-providers/) does the same.

To build on your own machine instead, send the result:

```bash
npx astro build
pail up dist
```

## What answers each path

The pail’s page shows the routes the build wrote. They are checked in order.

| Path | Answered by |
| --- | --- |
| A prerendered page, like `/about` | Its file. |
| A file from `public`, and the build’s assets under `/_astro` | Its file. |
| Everything else | The function: on-demand pages, API routes, redirects, and the site’s 404 page. |

Middleware runs for what the function answers. For a prerendered page it ran once, during the build.

## Settings

The adapter takes these. All are optional.

```js
adapter: pail({
  memory: '512MB',
  timeout: '30s',
  env: { GREETING: 'hello' },
}),
```

| Option | Default | What it sets |
| --- | --- | --- |
| `name` | `ssr` | The function’s name, as it shows on the pail’s page and in its output. |
| `memory` | `256MB` | How much memory each copy gets. |
| `timeout` | `10s` | How long one request may take. |
| `idle` | `5m` | How long a copy waits for another request before it sleeps. |
| `max` | `4` | How many copies may run at once. |
| `env` | none | Environment variables for the function. |
| `external` | none | Packages to leave out of the function’s files, for Pail to install where it runs. |

They mean what they mean for [any function](/deploying/functions/#settings).

## Packages

The build puts the site’s packages into the function’s own files, so nothing is installed when it runs. A package with a program compiled for one kind of machine can’t be carried that way. Name it in `external`, and Pail installs it where the function runs.

`sharp`, which Astro optimizes images with, is always left out and installed by Pail.

A package that reads files from beside its own code needs the same. These docs name `satori`, which draws their social images and loads its WebAssembly that way.

## See what it’s printing

What a page writes with `console.log` goes to the pail’s output, not into the response.

```bash
pail logs garden --output --follow
```

## Try it before you deploy

```bash
npx astro build
npx astro preview
```

`astro preview` answers the build the way Pail would: the same routes, and the function run afresh for each request.

## What it can’t do

A function starts the program afresh for every request. That shapes what an on-demand page can do.

- **Be instant.** Each request starts Node and loads the site before it renders. Prerender what you can.
- **Stream.** Pail sends the response when the page has finished rendering.
- **Remember.** Nothing is kept between requests. Astro’s sessions need a store somewhere else, set with `session.driver`.
- **Optimize an image on demand, on a pail only your network can reach.** The function fetches the original from the pail’s own address, and a function can’t reach your network. Images in prerendered pages are optimized by the build and are not affected.

For a busy site, or one that needs any of these, run Astro’s Node adapter in [a container](/deploying/containers/) instead.
