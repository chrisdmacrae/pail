// Pail runs this for each request to a Node function, with the function's
// own file as its argument. A file whose default export is a function is a
// handler: it is called with the request and a response to fill in. Any
// other file is a program that has answered for itself, by writing CGI.

import { writeSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const stdout = process.stdout;
const env = process.env;

// Which kind the file is isn't known until it has loaded, so what it writes
// to standard output meanwhile is held: the response if it is a program, and
// something for the pail's output if it is a handler.
let held = [];
stdout.write = (chunk, encoding, done) => {
  if (typeof encoding === 'function') [encoding, done] = [undefined, encoding];
  held.push(typeof chunk === 'string' ? Buffer.from(chunk, encoding) : Buffer.from(chunk));
  done?.();
  return true;
};
// A program that exits while it loads has written all it is going to.
process.on('exit', () => held && respond(Buffer.concat(held)));

/** Writes to standard output itself, all of it, before returning. */
function respond(bytes) {
  for (let at = 0; at < bytes.length; ) {
    try {
      at += writeSync(1, bytes, at);
    } catch (err) {
      if (err.code !== 'EAGAIN') throw err;
    }
  }
}

async function body() {
  if (!(Number(env.CONTENT_LENGTH) > 0)) return Buffer.alloc(0);
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks);
}

/** The request, from CGI's variables. */
function request(body) {
  const headers = {};
  for (const [key, value] of Object.entries(env)) {
    if (key.startsWith('HTTP_')) headers[key.slice(5).toLowerCase().replaceAll('_', '-')] = value;
  }
  if (env.CONTENT_TYPE) headers['content-type'] = env.CONTENT_TYPE;
  if (body.length) headers['content-length'] = String(body.length);
  const search = env.QUERY_STRING || '';
  const pathname = env.PATH_INFO || '/';
  return {
    method: (env.REQUEST_METHOD || 'GET').toUpperCase(),
    path: pathname,
    url: env.REQUEST_URI || (search ? `${pathname}?${search}` : pathname),
    query: Object.fromEntries(new URLSearchParams(search)),
    headers,
    body,
    text: () => body.toString('utf8'),
    json: () => JSON.parse(body.toString('utf8')),
    form: () => Object.fromEntries(new URLSearchParams(body.toString('utf8'))),
  };
}

/** The response a handler fills in. */
class Response {
  #status = 200;
  /** @type {[string, string][]} */
  #headers = [];
  #body = Buffer.alloc(0);
  sent = false;

  status(code) {
    if (!Number.isInteger(code) || code < 200 || code > 599) {
      throw new TypeError(`res.status takes a status from 200 to 599, like 404. Got ${code}.`);
    }
    this.#status = code;
    return this;
  }

  /** Sets a header, in place of any by that name. */
  set(name, value) {
    this.#headers = this.#headers.filter(([have]) => have.toLowerCase() !== String(name).toLowerCase());
    return this.append(name, value);
  }

  /** Adds a header, beside any by that name: one Set-Cookie after another. */
  append(name, value) {
    if (!/^[\w!#$%&'*+.^`|~-]+$/.test(name)) throw new TypeError(`${JSON.stringify(name)} isn't a header's name.`);
    if (/[\r\n]/.test(value)) throw new TypeError(`The ${name} header can't hold a line break.`);
    this.#headers.push([String(name), String(value)]);
    return this;
  }

  contentType(type) {
    return this.set('Content-Type', type);
  }

  /** Sends text or bytes as they are, and anything else as JSON. */
  send(body) {
    if (this.sent) throw new Error('The response was already sent.');
    let type = 'application/json';
    if (body === undefined || body === null) {
      type = '';
    } else if (typeof body === 'string') {
      this.#body = Buffer.from(body, 'utf8');
      type = 'text/plain; charset=utf-8';
    } else if (body instanceof Uint8Array) {
      this.#body = Buffer.from(body);
      type = 'application/octet-stream';
    } else {
      this.#body = Buffer.from(JSON.stringify(body), 'utf8');
    }
    if (type && !this.#headers.some(([name]) => name.toLowerCase() === 'content-type')) this.contentType(type);
    this.sent = true;
    return this;
  }

  json(value) {
    if (!this.#headers.some(([name]) => name.toLowerCase() === 'content-type')) this.contentType('application/json');
    return this.send(JSON.stringify(value));
  }

  redirect(location, status = 302) {
    return this.status(status).set('Location', location).send();
  }

  toCGI() {
    const lines = [`Status: ${this.#status}`, ...this.#headers.map(([name, value]) => `${name}: ${value}`)];
    return Buffer.concat([Buffer.from(`${lines.join('\r\n')}\r\n\r\n`, 'utf8'), this.#body]);
  }
}

const loaded = await import(pathToFileURL(path.resolve(process.argv[2])).href);
let handler = loaded.default;
// CommonJS that sets exports.default, as compiled TypeScript does.
if (typeof handler !== 'function' && typeof handler?.default === 'function') handler = handler.default;

const early = Buffer.concat(held);
held = null;
if (typeof handler !== 'function') {
  delete stdout.write;
  if (early.length) stdout.write(early);
} else {
  // Standard output is the response, so what the handler prints goes to
  // standard error instead: the pail's output.
  stdout.write = process.stderr.write.bind(process.stderr);
  if (early.length) process.stderr.write(early);
  try {
    const res = new Response();
    const result = await handler(request(await body()), res);
    if (!res.sent && result !== undefined && result !== null && result !== res) res.send(result);
    respond(res.toCGI());
  } catch (err) {
    console.error(err);
    process.exit(1);
  }
  // Nothing waits for what the handler left open, a database connection or
  // a timer.
  process.exit(0);
}
