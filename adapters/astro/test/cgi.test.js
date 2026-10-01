import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseCGI, requestFromCGI, responseToCGI, urlFromCGI } from '../src/cgi.js';

const base = { REQUEST_METHOD: 'GET', REQUEST_SCHEME: 'http', HTTP_HOST: 'garden.pail.lan', REQUEST_URI: '/' };

test('a request is rebuilt from CGI’s variables', async () => {
  const body = new TextEncoder().encode('{"n":1}');
  const request = requestFromCGI(
    {
      ...base,
      REQUEST_METHOD: 'post',
      REQUEST_URI: '/api/notes?page=2',
      CONTENT_TYPE: 'application/json',
      CONTENT_LENGTH: '7',
      HTTP_USER_AGENT: 'curl/8',
      HTTP_X_REQUEST_ID: 'abc',
      HTTP_COOKIE: 'a=1; b=2',
      PATH: '/usr/bin',
    },
    body,
  );
  assert.equal(request.method, 'POST');
  assert.equal(request.url, 'http://garden.pail.lan/api/notes?page=2');
  assert.equal(request.headers.get('host'), 'garden.pail.lan');
  assert.equal(request.headers.get('user-agent'), 'curl/8');
  assert.equal(request.headers.get('x-request-id'), 'abc');
  assert.equal(request.headers.get('cookie'), 'a=1; b=2');
  assert.equal(request.headers.get('content-type'), 'application/json');
  assert.equal(request.headers.has('path'), false);
  assert.deepEqual(await request.json(), { n: 1 });
});

test('a GET has no body, whatever arrived', () => {
  const request = requestFromCGI(base, new Uint8Array([1, 2, 3]));
  assert.equal(request.body, null);
});

test('the address is the one the visitor used', () => {
  assert.equal(urlFromCGI({ ...base, HTTP_HOST: 'garden.localhost:8080' }).href, 'http://garden.localhost:8080/');
  assert.equal(urlFromCGI({ ...base, REQUEST_SCHEME: 'https', HTTPS: 'on' }).href, 'https://garden.pail.lan/');
  // Behind a proxy that upgrades to HTTPS, Pail itself hears plain HTTP.
  assert.equal(urlFromCGI({ ...base, HTTP_X_FORWARDED_PROTO: 'https' }).protocol, 'https:');
  assert.equal(urlFromCGI({ ...base, HTTP_X_FORWARDED_PROTO: 'https, http' }).protocol, 'https:');
  // Without REQUEST_URI, the path and the query make it.
  const parts = { ...base, REQUEST_URI: undefined, PATH_INFO: '/a b', QUERY_STRING: 'q=1' };
  assert.equal(urlFromCGI(parts).href, 'http://garden.pail.lan/a%20b?q=1');
  assert.equal(urlFromCGI({ SERVER_NAME: 'pail.lan', SERVER_PORT: '8080' }).href, 'http://pail.lan:8080/');
});

test('a header the runtime refuses is left out, not fatal', () => {
  const request = requestFromCGI({ ...base, HTTP_X_BAD: 'a\nb', HTTP_X_GOOD: 'ok' });
  assert.equal(request.headers.has('x-bad'), false);
  assert.equal(request.headers.get('x-good'), 'ok');
});

test('a response is written as CGI, and read back as Pail reads it', async () => {
  const headers = new Headers({
    'Content-Type': 'application/octet-stream',
    'Content-Length': '999',
    'X-Kind': 'test',
  });
  headers.append('Set-Cookie', 'a=1; Expires=Wed, 21 Oct 2026 07:28:00 GMT');
  headers.append('Set-Cookie', 'b=2; Path=/');
  const bytes = new Uint8Array([0, 255, 13, 10, 13, 10, 65]);
  const out = await responseToCGI(new Response(bytes, { status: 201, headers }));

  assert.ok(out.toString('latin1').startsWith('Status: 201\r\n'));
  const { status, headers: read, body } = parseCGI(out);
  assert.equal(status, 201);
  assert.deepEqual(read, [
    ['content-type', 'application/octet-stream'],
    ['x-kind', 'test'],
    ['set-cookie', 'a=1; Expires=Wed, 21 Oct 2026 07:28:00 GMT'],
    ['set-cookie', 'b=2; Path=/'],
  ]);
  assert.deepEqual(new Uint8Array(body), bytes);
});

test('a response with no body still ends its headers', async () => {
  const out = await responseToCGI(new Response(null, { status: 303, headers: { Location: '/next' } }));
  assert.equal(out.toString(), 'Status: 303\r\nlocation: /next\r\n\r\n');
});

test('output that isn’t CGI is refused, as Pail refuses it', () => {
  assert.throws(() => parseCGI(Buffer.from('<h1>hello</h1>')), /header lines/);
  assert.throws(() => parseCGI(Buffer.from('hello\n\nworld')), /header lines/);
  assert.throws(() => parseCGI(Buffer.from('Status: 99\n\n')), /isn't a status/);
  assert.equal(parseCGI(Buffer.from('Location: /x\n\n')).status, 302);
  assert.deepEqual(parseCGI(Buffer.from('X-A: 1\n\nbody')).headers, [
    ['x-a', '1'],
    ['content-type', 'text/plain; charset=utf-8'],
  ]);
});
