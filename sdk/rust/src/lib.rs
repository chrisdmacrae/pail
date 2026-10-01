//! Write a Pail function in Rust: a request to read and a response to fill
//! in.
//!
//! A Pail function speaks CGI: the request arrives as environment variables
//! and standard input, and the response leaves on standard output as header
//! lines, a blank line, then the body. [`handle`] does both ends of that.
//!
//! ```no_run
//! fn main() {
//!     pail_fn::handle(|req, res| {
//!         res.status(200);
//!         res.content_type("application/json");
//!         res.send(format!(r#"{{"path": "{}"}}"#, req.path));
//!     });
//! }
//! ```
//!
//! Standard output is the response, so print with `eprintln!`: standard
//! error goes to the pail's output.

use std::collections::HashMap;
use std::io::{self, Read, Write};

/// The request, from CGI's variables.
#[derive(Debug, Clone, Default)]
pub struct Request {
    /// `GET`, `POST` and so on.
    pub method: String,
    /// The full path asked for, like `/api/notes/7`.
    pub path: String,
    /// The path and what follows the `?`.
    pub url: String,
    /// What follows the `?`, by name. Of two with one name, the last.
    pub query: HashMap<String, String>,
    /// The request's headers, by name in lower case.
    pub headers: HashMap<String, String>,
    /// The request's body.
    pub body: Vec<u8>,
}

impl Request {
    /// The request these variables and this body describe.
    pub fn from_cgi<I, K, V>(env: I, body: Vec<u8>) -> Request
    where
        I: IntoIterator<Item = (K, V)>,
        K: Into<String>,
        V: Into<String>,
    {
        let env: HashMap<String, String> = env.into_iter().map(|(k, v)| (k.into(), v.into())).collect();
        let var = |name: &str| env.get(name).filter(|v| !v.is_empty()).cloned();

        let mut headers = HashMap::new();
        for (key, value) in &env {
            if let Some(name) = key.strip_prefix("HTTP_") {
                headers.insert(name.to_lowercase().replace('_', "-"), value.clone());
            }
        }
        if let Some(kind) = var("CONTENT_TYPE") {
            headers.insert("content-type".into(), kind);
        }
        if !body.is_empty() {
            headers.insert("content-length".into(), body.len().to_string());
        }

        let path = var("PATH_INFO").unwrap_or_else(|| "/".into());
        let search = var("QUERY_STRING").unwrap_or_default();
        let url = var("REQUEST_URI").unwrap_or_else(|| match search.as_str() {
            "" => path.clone(),
            search => format!("{path}?{search}"),
        });
        Request {
            method: var("REQUEST_METHOD").unwrap_or_else(|| "GET".into()).to_uppercase(),
            query: decode_form(search.as_bytes()),
            path,
            url,
            headers,
            body,
        }
    }

    /// A header, whatever case its name is asked for in.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.get(&name.to_lowercase()).map(String::as_str)
    }

    /// The body as text.
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.body).into_owned()
    }

    /// The body as the fields of a form, by name.
    pub fn form(&self) -> HashMap<String, String> {
        decode_form(&self.body)
    }
}

/// `a=1&b=two+words` as its names and values.
fn decode_form(encoded: &[u8]) -> HashMap<String, String> {
    let mut fields = HashMap::new();
    for pair in encoded.split(|&b| b == b'&').filter(|pair| !pair.is_empty()) {
        let mut halves = pair.splitn(2, |&b| b == b'=');
        let name = decode(halves.next().unwrap_or_default());
        fields.insert(name, decode(halves.next().unwrap_or_default()));
    }
    fields
}

fn decode(encoded: &[u8]) -> String {
    let hex = |b: u8| (b as char).to_digit(16).map(|d| d as u8);
    let mut out = Vec::with_capacity(encoded.len());
    let mut at = 0;
    while at < encoded.len() {
        let escaped = match encoded[at..] {
            [b'%', high, low, ..] => hex(high).zip(hex(low)),
            _ => None,
        };
        match (encoded[at], escaped) {
            (_, Some((high, low))) => {
                out.push(high << 4 | low);
                at += 2;
            }
            (b'+', _) => out.push(b' '),
            (b, _) => out.push(b),
        }
        at += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The response a handler fills in.
#[derive(Debug, Clone)]
pub struct Response {
    status: u16,
    headers: Vec<(String, String)>,
    body: Vec<u8>,
    sent: bool,
}

impl Default for Response {
    fn default() -> Self {
        Response { status: 200, headers: Vec::new(), body: Vec::new(), sent: false }
    }
}

impl Response {
    pub fn new() -> Response {
        Response::default()
    }

    /// Sets the status. Without one it is 200.
    ///
    /// # Panics
    /// If the status isn't from 200 to 599.
    pub fn status(&mut self, code: u16) -> &mut Self {
        assert!((200..=599).contains(&code), "res.status takes a status from 200 to 599, like 404. Got {code}.");
        self.status = code;
        self
    }

    /// Sets a header, in place of any by that name.
    pub fn set(&mut self, name: &str, value: impl AsRef<str>) -> &mut Self {
        self.headers.retain(|(have, _)| !have.eq_ignore_ascii_case(name));
        self.append(name, value)
    }

    /// Adds a header, beside any by that name: one `Set-Cookie` after
    /// another.
    ///
    /// # Panics
    /// If the name isn't a header's name, or the value holds a line break.
    pub fn append(&mut self, name: &str, value: impl AsRef<str>) -> &mut Self {
        let value = value.as_ref();
        let token = |c: char| c.is_ascii_alphanumeric() || "!#$%&'*+.^_`|~-".contains(c);
        assert!(!name.is_empty() && name.chars().all(token), "{name:?} isn't a header's name.");
        assert!(!value.contains(['\r', '\n']), "The {name} header can't hold a line break.");
        self.headers.push((name.to_string(), value.to_string()));
        self
    }

    pub fn content_type(&mut self, kind: &str) -> &mut Self {
        self.set("Content-Type", kind)
    }

    /// Sends the body: text or bytes, as they are. With no `Content-Type`
    /// set, text is `text/plain` and anything else `application/octet-stream`.
    ///
    /// # Panics
    /// If the response was already sent.
    pub fn send(&mut self, body: impl AsRef<[u8]>) -> &mut Self {
        assert!(!self.sent, "The response was already sent.");
        let body = body.as_ref();
        if !body.is_empty() && !self.typed() {
            let text = std::str::from_utf8(body).is_ok();
            self.content_type(if text { "text/plain; charset=utf-8" } else { "application/octet-stream" });
        }
        self.body = body.to_vec();
        self.sent = true;
        self
    }

    /// Sends text that is already JSON, as `application/json`.
    pub fn json(&mut self, json: impl AsRef<str>) -> &mut Self {
        if !self.typed() {
            self.content_type("application/json");
        }
        self.send(json.as_ref())
    }

    /// Sends the visitor elsewhere, with a status like 302.
    pub fn redirect(&mut self, location: &str, status: u16) -> &mut Self {
        self.status(status).set("Location", location).send("")
    }

    /// Whether the response has been sent.
    pub fn sent(&self) -> bool {
        self.sent
    }

    fn typed(&self) -> bool {
        self.headers.iter().any(|(name, _)| name.eq_ignore_ascii_case("content-type"))
    }

    /// The response as CGI writes it.
    pub fn to_cgi(&self) -> Vec<u8> {
        let mut out = format!("Status: {}\r\n", self.status).into_bytes();
        for (name, value) in &self.headers {
            out.extend_from_slice(format!("{name}: {value}\r\n").as_bytes());
        }
        out.extend_from_slice(b"\r\n");
        out.extend_from_slice(&self.body);
        out
    }
}

/// Answers the request Pail ran this program for: reads it, calls the
/// handler with it and a response to fill in, and writes the response.
pub fn handle<F>(handler: F)
where
    F: FnOnce(&Request, &mut Response),
{
    let length: u64 = std::env::var("CONTENT_LENGTH").ok().and_then(|v| v.parse().ok()).unwrap_or(0);
    let mut body = Vec::new();
    if length > 0 {
        io::stdin().lock().take(length).read_to_end(&mut body).expect("read the request's body");
    }
    let req = Request::from_cgi(std::env::vars_os().filter_map(|(k, v)| Some((k.into_string().ok()?, v.into_string().ok()?))), body);
    let mut res = Response::new();
    handler(&req, &mut res);

    let mut out = io::stdout().lock();
    out.write_all(&res.to_cgi()).and_then(|_| out.flush()).expect("write the response");
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request() -> Request {
        Request::from_cgi(
            [
                ("REQUEST_METHOD", "post"),
                ("PATH_INFO", "/api/notes/7"),
                ("QUERY_STRING", "page=2&q=two+words&tag=a%2Fb&tag=last&bad=%zz%4"),
                ("REQUEST_URI", "/api/notes/7?page=2"),
                ("CONTENT_TYPE", "application/x-www-form-urlencoded"),
                ("HTTP_USER_AGENT", "tests"),
                ("HTTP_X_FORWARDED_PROTO", "https"),
                ("HOME", "/tmp"),
            ],
            b"name=Ada+L&note=caf%C3%A9".to_vec(),
        )
    }

    #[test]
    fn reads_a_request() {
        let req = request();
        assert_eq!((req.method.as_str(), req.path.as_str(), req.url.as_str()), ("POST", "/api/notes/7", "/api/notes/7?page=2"));
        assert_eq!(req.query["page"], "2");
        assert_eq!(req.query["q"], "two words");
        assert_eq!(req.query["tag"], "last");
        assert_eq!(req.query["bad"], "%zz%4");
        assert_eq!(req.header("User-Agent"), Some("tests"));
        assert_eq!(req.header("x-forwarded-proto"), Some("https"));
        assert_eq!(req.header("content-type"), Some("application/x-www-form-urlencoded"));
        assert_eq!(req.header("Content-Length"), Some("25"));
        assert_eq!(req.header("home"), None);
        assert_eq!(req.form()["name"], "Ada L");
        assert_eq!(req.form()["note"], "café");
        assert_eq!(req.text(), "name=Ada+L&note=caf%C3%A9");
    }

    #[test]
    fn a_request_with_nothing_said() {
        let req = Request::from_cgi([("QUERY_STRING", "a=1")], Vec::new());
        assert_eq!((req.method.as_str(), req.path.as_str(), req.url.as_str()), ("GET", "/", "/?a=1"));
        assert!(req.headers.is_empty() && req.body.is_empty());
    }

    #[test]
    fn writes_a_response() {
        let mut res = Response::new();
        res.status(201).content_type("application/json").send(r#"{"foo":"bar"}"#);
        assert!(res.sent());
        assert_eq!(res.to_cgi(), b"Status: 201\r\nContent-Type: application/json\r\n\r\n{\"foo\":\"bar\"}");

        let mut res = Response::new();
        res.append("Set-Cookie", "a=1").append("Set-Cookie", "b=2").set("X-One", "1").set("x-one", "2").send("hello");
        assert_eq!(res.to_cgi(), b"Status: 200\r\nSet-Cookie: a=1\r\nSet-Cookie: b=2\r\nx-one: 2\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello");

        let mut res = Response::new();
        res.send([0xff, 0x00]);
        assert_eq!(res.to_cgi(), b"Status: 200\r\nContent-Type: application/octet-stream\r\n\r\n\xff\x00");

        let mut res = Response::new();
        res.json("[1]");
        assert_eq!(res.to_cgi(), b"Status: 200\r\nContent-Type: application/json\r\n\r\n[1]");

        let mut res = Response::new();
        res.redirect("/elsewhere", 302);
        assert_eq!(res.to_cgi(), b"Status: 302\r\nLocation: /elsewhere\r\n\r\n");

        assert_eq!(Response::new().to_cgi(), b"Status: 200\r\n\r\n");
    }

    #[test]
    #[should_panic(expected = "can't hold a line break")]
    fn a_header_cant_hold_a_line_break() {
        Response::new().set("Location", "/x\r\nSet-Cookie: stolen=1");
    }

    #[test]
    #[should_panic(expected = "from 200 to 599")]
    fn a_status_has_to_be_one() {
        Response::new().status(99);
    }

    #[test]
    #[should_panic(expected = "already sent")]
    fn a_response_is_sent_once() {
        Response::new().send("one").send("two");
    }
}
