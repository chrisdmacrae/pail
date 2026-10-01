# pail-fn

Write a [Pail](https://github.com/chrisdmacrae/pail) function in Rust: a request to read, and a response to fill in.

The guide is in Pail's docs, under Deploying: `docs/src/content/docs/deploying/functions.md`.

## Use it

```toml
# Cargo.toml
[dependencies]
pail-fn = "0.1"
```

```rust
// src/main.rs
fn main() {
    pail_fn::handle(|req, res| {
        res.status(200);
        res.content_type("application/json");
        res.send(format!(r#"{{"path": "{}"}}"#, req.path));
    });
}
```

Then say where the function is in `pail.json`, and `pail up`.

## The request

| On `req` | What it holds |
| --- | --- |
| `method` | `GET`, `POST` and so on. |
| `path` | The full path asked for, like `/api/notes/7`. |
| `url` | The path, and what follows the `?`. |
| `query` | What follows the `?`, by name. |
| `headers`, `header(name)` | The request's headers, by name in lower case. `header` takes a name in any case. |
| `body` | The body, as bytes. |
| `text()` | The body as text. |
| `form()` | The body as a form's fields, by name. |

## The response

Each of these returns `res`, so they chain.

| On `res` | What it does |
| --- | --- |
| `status(code)` | Sets the status. Without it the status is 200. |
| `set(name, value)` | Sets a header, in place of any by that name. |
| `append(name, value)` | Adds a header beside any by that name. |
| `content_type(kind)` | Sets the `Content-Type`. |
| `send(body)` | Sends text or bytes, once. |
| `json(text)` | Sends text that is already JSON, as `application/json`. |
| `redirect(location, status)` | Sends the visitor elsewhere. |

- The crate depends on nothing, so it reads and writes no JSON of its own. Use `serde_json` for that, and hand `json` what it makes.
- Standard output is the response. Print with `eprintln!`, which goes to the pail's output.
- A status that isn't one, a header with a line break in it, and a second `send` all panic. A handler that panics answers with a 500, and the pail's output says why.

## How it works

A Pail function speaks CGI: the request arrives as environment variables and standard input, and the response leaves on standard output as header lines, a blank line, then the body. `handle` reads one and writes the other. `Request::from_cgi` and `Response::to_cgi` are the two halves, for tests.

```bash
cargo test
```

## Releases

A push to `main` that changes `src` or `Cargo.toml` publishes the crate to crates.io: `.github/workflows/release-rust.yml`. The version is the one in `Cargo.toml` the first time, and the next patch each time after. To start a new minor or major, change `Cargo.toml`.
