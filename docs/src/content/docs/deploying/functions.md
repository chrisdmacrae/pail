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

Functions need `/dev/kvm` on the server, or a Pail on [Docker](/running/docker/) or [Podman](/running/podman/). [Running Pail](/running/) covers both.

## 1. Write the handler

A function is a handler: Pail calls it with the request, and a response for it to fill in.

In Node, as `fn/api/index.js`:

```js
export default function (req, res) {
  res.status(200);
  res.contentType("application/json");

  return res.send(JSON.stringify({ method: req.method, path: req.path }));
}
```

[Each language](#handlers-in-each-language) has its own way to write one.

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

## The request

`req` is what was asked for.

| On `req` | What it holds |
| --- | --- |
| `method` | `GET`, `POST` and so on. |
| `path` | The full path asked for, like `/api/notes/7`. |
| `url` | The path, and what follows the `?`. |
| `query` | What follows the `?`, by name: `req.query.page`. |
| `headers` | The request’s headers, by name in lower case: `req.headers["user-agent"]`. |
| `body` | The body, as bytes. |
| `text()` | The body as text. |
| `json()` | The body, read as JSON. |
| `form()` | The body as a form’s fields, by name. |

## The response

`res` is the answer. Each of these returns `res`, so they chain: `res.status(404).send("No such recipe.")`.

| On `res` | What it does |
| --- | --- |
| `status(code)` | Sets the status. Without it the status is 200. |
| `set(name, value)` | Sets a header, in place of any by that name. |
| `append(name, value)` | Adds a header beside any by that name: one `Set-Cookie` after another. |
| `contentType(type)` | Sets the `Content-Type`. It is `content_type` in Python, Ruby and Rust. |
| `send(body)` | Sends the body, once. Text and bytes go as they are; anything else goes as JSON. |
| `json(value)` | Sends a value as JSON. |
| `redirect(location, status)` | Sends the visitor elsewhere. The status is 302 unless you give one. |

- Without a `Content-Type`, text is `text/plain`, bytes are `application/octet-stream`, and anything else is `application/json`.
- A handler can return its answer instead of sending it: `return { ok: true }`.
- What the handler prints goes to the pail’s output, not the response.
- A handler that throws answers with a 500, and the pail’s output says why.

## Handlers in each language

### Node

The file’s default export is the handler. It can be `async`. `module.exports = function (req, res) {}` works too.

```js
export default async function (req, res) {
  const note = req.json();
  return res.status(201).json({ saved: note.title });
}
```

### Python

Define `handler` in `main.py`. It can be `async`.

```python
def handler(req, res):
    note = req.json()
    return res.status(201).json({"saved": note["title"]})
```

### Ruby

Define `handler` in `main.rb`.

```ruby
def handler(req, res)
  note = req.json
  res.status(201).json({ saved: note["title"] })
end
```

### Go

Go’s standard library has the handler already. Write an `http.HandlerFunc`, and serve it with `net/http/cgi`.

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/cgi"
)

func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
}

func main() {
	cgi.Serve(http.HandlerFunc(handler))
}
```

### Rust

Add the `pail-fn` crate to `Cargo.toml`:

```toml
[dependencies]
pail-fn = "0.1"
```

Then hand `pail_fn::handle` the handler:

```rust
fn main() {
    pail_fn::handle(|req, res| {
        res.status(200);
        res.content_type("application/json");
        res.send(format!(r#"{{"path": "{}"}}"#, req.path));
    });
}
```

The crate depends on nothing, so it reads and writes no JSON of its own: `json` takes text that is JSON already, and `redirect` always takes its status. Print with `eprintln!`, since standard output is the response.

## Or write CGI yourself

Under the handler is CGI, and a function can speak it directly. The program reads the request from environment variables and standard input, and writes the response to standard output: header lines, a blank line, then the body. Any language can do it, and it is how a shell function answers.

In Python, a `main.py` with no `handler` is run as a program:

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

So is a Node file with no default export, a Ruby file with no `handler`, and anything you run with a `cmd` of your own.

### What the program is given

| Variable | What it holds |
| --- | --- |
| `REQUEST_METHOD` | `GET`, `POST` and so on. |
| `PATH_INFO` | The full path asked for, like `/api/notes/7`. |
| `QUERY_STRING` | What follows the `?`, like `page=2`. |
| `CONTENT_TYPE`, `CONTENT_LENGTH` | The body’s type and its length in bytes. |
| `HTTP_<NAME>` | Each request header, upper-cased, dashes as underscores: `HTTP_USER_AGENT`. |
| `PAIL_NAME`, `PAIL_DEPLOY` | The pail, and the deploy that is answering. |

The request’s body is on standard input. Anything you set under `env` in `pail.json` is there too, and a handler has all of these as well: `process.env.PAIL_NAME`.

### What the program writes

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
| Python | `requirements.txt` or `main.py` | `pip install -r requirements.txt` | `main.py` |
| Node | `package.json` or `index.js` | `npm ci` | the `main` in `package.json`, else `index.js` |
| Ruby | `Gemfile` or `main.rb` | `bundle install` | `main.rb` |
| Go | `go.mod` | `go build` | the built program |
| Rust | `Cargo.toml` | `cargo build --release` | the built program |
| Shell | `main.sh` | nothing | `sh main.sh` |

`src` can also be a single file, such as `./fn/hello.py`. Pail goes by its extension.

If Pail guesses wrong, say so with `lang`. To run something other than the usual file, give a `cmd`. Pail runs a `cmd` as it is, so its program [writes CGI itself](#or-write-cgi-yourself).

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
| It exited with a status | The program failed, or the handler threw. What it wrote to standard error, in the output, says why. |
| It ran longer than its timeout | Make it faster, or raise `timeout`. |
| It ran out of memory | Raise `memory`. |
| It answered in a way Pail couldn’t read | The file has no handler, and didn’t write CGI either. Export a handler, or write header lines, then a blank line, then the body. |
| It is busy | Every copy was in use for as long as the request could wait. Raise `max`. |
