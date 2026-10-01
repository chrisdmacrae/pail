---
title: Write a function
lead: "A function is a small program Pail runs once for each request. No server to write, no Dockerfile, and it costs nothing while nobody is asking."
section: Deploying
order: 4
navLabel: Write a function
next:
  href: /deploying/containers/
  label: Run a container
---

## When to use one

Use a function when you have a script that answers a request: a form handler, a webhook, a small API beside a static site. Pail starts it when a request arrives and lets it sleep when none do.

For a server that stays up, websockets, or data that has to last, [run a container](/deploying/containers/) instead.

Functions need `/dev/kvm` on the server. [Running Pail](/running/) covers that.

## 1. Write the program

A function reads the request from environment variables and standard input, and writes the response to standard output: header lines, a blank line, then the body. This is CGI, and any language can do it.

In Python, as `fn/api/main.py`:

```python
import json, os, sys

body = sys.stdin.buffer.read()
print("Content-Type: application/json")
print()
print(json.dumps({
    "method": os.environ.get("REQUEST_METHOD", ""),
    "path": os.environ.get("PATH_INFO", ""),
    "bytes": len(body),
}))
```

## 2. Add a pail.json

Say where the function’s source is, and which paths it answers.

```json
{
  "static": "./build",
  "functions": {
    "api": { "src": "./fn/api" }
  },
  "routes": [
    { "path": "/api/*", "to": "function:api" },
    { "path": "/*", "to": "static" }
  ]
}
```

With one function and nothing else, you can leave `routes` out: it answers every path.

## 3. Deploy it

```bash
pail up
```

Pail works out the language, installs what the function depends on, and gets it ready. The first request wakes it; after five minutes with no requests it goes back to sleep.

## What the program is given

| Variable | What it holds |
| --- | --- |
| `REQUEST_METHOD` | `GET`, `POST` and so on. |
| `PATH_INFO` | The full path asked for, like `/api/notes/7`. |
| `QUERY_STRING` | What follows the `?`, like `page=2`. |
| `CONTENT_TYPE`, `CONTENT_LENGTH` | The body’s type and its length in bytes. |
| `HTTP_<NAME>` | Each request header, upper-cased, dashes as underscores: `HTTP_USER_AGENT`. |
| `PAIL_NAME`, `PAIL_DEPLOY` | The pail, and the deploy that is answering. |

The request’s body is on standard input. Anything you set under `env` in `pail.json` is there too.

## What the program writes

Header lines, a blank line, then the body.

```
Status: 404
Content-Type: text/plain

No such recipe.
```

- `Status` sets the status. Without it the status is 200.
- Without a `Content-Type`, the response is `text/plain`.
- A `Location` header with no `Status` is a redirect.
- Anything written to standard error goes to the pail’s output, not the response.

## Languages

Pail tells the language from what the source folder holds.

| Language | Pail sees | Before it runs | It runs |
| --- | --- | --- | --- |
| Python | `requirements.txt` or `main.py` | `pip install -r requirements.txt` | `python3 main.py` |
| Node | `package.json` or `index.js` | `npm ci` | the `main` in `package.json`, else `index.js` |
| Ruby | `Gemfile` or `main.rb` | `bundle install` | `ruby main.rb` |
| Go | `go.mod` | `go build` | the built program |
| Rust | `Cargo.toml` | `cargo build --release` | the built program |
| Shell | `main.sh` | nothing | `sh main.sh` |

`src` can also be a single file, such as `./fn/hello.py`. Pail goes by its extension.

If Pail guesses wrong, say so with `lang`. To run something other than the usual file, give a `cmd`.

## Settings

Each function takes these in `pail.json`. Only `src` is required.

| Field | Default | What it sets |
| --- | --- | --- |
| `src` | none | The function’s source: a folder, or one file. |
| `lang` | detected | `python`, `node`, `ruby`, `go`, `rust` or `shell`. |
| `cmd` | the language’s | What to run for each request, like `"python3 app.py"`. |
| `timeout` | `10s` | How long one request may take. |
| `memory` | `128MB` | How much memory it gets. |
| `idle` | `5m` | How long it waits for another request before it sleeps. |
| `max` | `4` | How many copies may run at once. |
| `env` | none | Environment variables for the program. |

The server sets the most memory a function may ask for: 1GB unless `PAIL_MAX_FUNCTION_MEMORY` says otherwise.

## Copies

Each copy of a function handles one request at a time. When requests overlap, Pail starts more copies, up to `max`. Past that, a request waits for a copy to come free.

So five requests at once, with the default `max` of 4, start four copies. The fifth waits for the first to finish. A request still waiting when its `timeout` runs out gets a 503.

Set `max` to 1 for a function that must never run twice at once. Raise it for a busy one.

## What a function can’t do

- **Remember.** Every request starts the program afresh. Files in `/tmp` may still be there for the next request, but nothing promises it.
- **Keep data.** There is no disk that lasts. Use [a container’s `data`](/deploying/containers/) for that.
- **Stream.** Pail sends the response when the program has finished, so it can tell whether it failed.
- **Reach your network.** A function can reach the internet, and nothing at home.

## See what it’s printing

What the program writes to standard error shows on the pail’s page, under Output. From a terminal:

```bash
pail logs dash --output --follow
```

## When a request fails

A failed request gets a 500 that says what happened, and the pail’s output says the same.

| It says | What to do |
| --- | --- |
| It exited with a status | The program failed. What it wrote to standard error, in the output, says why. |
| It ran longer than its timeout | Make it faster, or raise `timeout`. |
| It ran out of memory | Raise `memory`. |
| It answered in a way Pail couldn’t read | Write header lines, then a blank line, then the body. |
| It is busy | Every copy was in use for as long as the request could wait. Raise `max`. |
