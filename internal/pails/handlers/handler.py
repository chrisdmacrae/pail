# Pail runs this for each request to a Python function, with the function's
# own file as its argument. A file that defines handler(req, res) is a
# handler: it is called with the request and a response to fill in. Any
# other file is a program that has answered for itself, by writing CGI.

import json
import os
import re
import sys
import types
from urllib.parse import parse_qsl


class Request:
    """The request, from CGI's variables."""

    def __init__(self, env, body):
        self.method = env.get("REQUEST_METHOD", "GET").upper()
        self.path = env.get("PATH_INFO") or "/"
        search = env.get("QUERY_STRING", "")
        self.url = env.get("REQUEST_URI") or (self.path + "?" + search if search else self.path)
        self.query = dict(parse_qsl(search, keep_blank_values=True))
        self.headers = {key[5:].lower().replace("_", "-"): value for key, value in env.items() if key.startswith("HTTP_")}
        if env.get("CONTENT_TYPE"):
            self.headers["content-type"] = env["CONTENT_TYPE"]
        if body:
            self.headers["content-length"] = str(len(body))
        self.body = body

    def text(self):
        return self.body.decode("utf-8")

    def json(self):
        return json.loads(self.body)

    def form(self):
        return dict(parse_qsl(self.text(), keep_blank_values=True))


class Response:
    """The response a handler fills in."""

    def __init__(self):
        self._status = 200
        self._headers = []
        self._body = b""
        self.sent = False

    def status(self, code):
        if not isinstance(code, int) or not 200 <= code <= 599:
            raise ValueError(f"res.status takes a status from 200 to 599, like 404. Got {code!r}.")
        self._status = code
        return self

    def set(self, name, value):
        """Sets a header, in place of any by that name."""
        self._headers = [(have, was) for have, was in self._headers if have.lower() != str(name).lower()]
        return self.append(name, value)

    def append(self, name, value):
        """Adds a header, beside any by that name: one Set-Cookie after another."""
        name, value = str(name), str(value)
        if not re.fullmatch(r"[\w!#$%&'*+.^`|~-]+", name, re.ASCII):
            raise ValueError(f"{name!r} isn't a header's name.")
        if "\r" in value or "\n" in value:
            raise ValueError(f"The {name} header can't hold a line break.")
        self._headers.append((name, value))
        return self

    def content_type(self, type):
        return self.set("Content-Type", type)

    def _typed(self):
        return any(name.lower() == "content-type" for name, _ in self._headers)

    def send(self, body=None):
        """Sends text or bytes as they are, and anything else as JSON."""
        if self.sent:
            raise RuntimeError("The response was already sent.")
        type = "application/json"
        if body is None:
            type = ""
        elif isinstance(body, str):
            self._body, type = body.encode("utf-8"), "text/plain; charset=utf-8"
        elif isinstance(body, (bytes, bytearray, memoryview)):
            self._body, type = bytes(body), "application/octet-stream"
        else:
            self._body = json.dumps(body).encode("utf-8")
        if type and not self._typed():
            self.content_type(type)
        self.sent = True
        return self

    def json(self, value):
        if not self._typed():
            self.content_type("application/json")
        return self.send(json.dumps(value))

    def redirect(self, location, status=302):
        return self.status(status).set("Location", location).send()

    def _cgi(self):
        lines = [f"Status: {self._status}"] + [f"{name}: {value}" for name, value in self._headers]
        return ("\r\n".join(lines) + "\r\n\r\n").encode("utf-8") + self._body


def _write(fd, data):
    data = memoryview(data)
    while data:
        data = data[os.write(fd, data):]


def _main(path):
    env = os.environ
    # Which kind the file is isn't known until it has run, so what it writes
    # to standard output meanwhile is held in a file: the response if it is a
    # program, and something for the pail's output if it is a handler.
    out = os.dup(1)
    name = os.path.join(env.get("TMPDIR") or "/tmp", f".pail-held-{os.getpid()}")
    held = os.open(name, os.O_RDWR | os.O_CREAT | os.O_EXCL, 0o600)
    os.unlink(name)
    os.dup2(held, 1)

    # The file runs as the program it would be were it run by itself.
    module = types.ModuleType("__main__")
    module.__file__ = path
    sys.modules["__main__"] = module
    sys.argv = [path]
    sys.path[0] = os.path.dirname(os.path.abspath(path))
    handler = None
    try:
        with open(path, "rb") as source:
            code = compile(source.read(), path, "exec")
        exec(code, module.__dict__)
        handler = getattr(module, "handler", None)
    finally:
        # Standard output is the response, so what a handler prints goes to
        # standard error instead: the pail's output.
        to = 2 if callable(handler) else out
        sys.stdout.flush()
        os.dup2(to, 1)
        os.lseek(held, 0, os.SEEK_SET)
        while chunk := os.read(held, 1 << 16):
            _write(to, chunk)
        os.close(held)
    if not callable(handler):
        return

    length = int(env.get("CONTENT_LENGTH") or 0)
    res = Response()
    result = handler(Request(env, sys.stdin.buffer.read(length) if length > 0 else b""), res)
    if hasattr(result, "__await__"):
        import asyncio

        result = asyncio.run(result)
    if not res.sent and result is not None and result is not res:
        res.send(result)
    sys.stdout.flush()
    _write(out, res._cgi())


if __name__ == "__main__":
    _main(sys.argv[1])
