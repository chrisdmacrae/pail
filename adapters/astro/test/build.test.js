// Builds the fixture site, then asks it for pages the way Pail would: files
// by pail.json's routes, and the function run once for each request.

import assert from 'node:assert/strict';
import { access, readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { build } from 'astro';
import { createPail } from '../src/emulate.js';

const root = fileURLToPath(new URL('./fixtures/site/', import.meta.url));
const output = [];
let server;
let origin;

before(async () => {
  await build({ root, logLevel: 'error' });
  const answer = await createPail(`${root}dist`, { log: (line) => output.push(line) });
  server = createServer((req, res) => answer(req, res));
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
});

after(() => server?.close());

const get = (path, init) => fetch(origin + path, { redirect: 'manual', ...init });

test('the build writes a pail.json Pail can deploy', async () => {
  const pail = JSON.parse(await readFile(`${root}dist/pail.json`, 'utf8'));
  assert.equal(pail.static, './client');
  assert.deepEqual(pail.functions, {
    ssr: {
      src: './server',
      lang: 'node',
      cmd: ['node', 'entry.mjs'],
      memory: '256MB',
      timeout: '20s',
      env: { NODE_COMPILE_CACHE: '/tmp/node-compile-cache', SECRET: 'from pail.json' },
    },
  });
  assert.deepEqual(pail.routes, [
    { path: '/404', to: 'static' },
    { path: '/', to: 'static' },
    { path: '/robots.txt', to: 'static' },
    { path: '/about/*', to: 'static' },
    // /blog/[slug] is rendered on demand, so only the blog's own page is a file.
    { path: '/blog', to: 'static' },
    { path: '/styled/*', to: 'static' },
    { path: '/*', to: 'function:ssr' },
  ]);
});

test('the function runs with nothing installed but what Pail installs for it', async () => {
  // sharp has a program compiled for the machine it runs on.
  const pkg = JSON.parse(await readFile(`${root}dist/server/package.json`, 'utf8'));
  assert.deepEqual(Object.keys(pkg.dependencies), ['sharp']);
  assert.match(pkg.dependencies.sharp, /^\d+\.\d+\.\d+/);
});

test('prerendered pages are served as files', async () => {
  const home = await get('/');
  assert.equal(home.status, 200);
  assert.match(await home.text(), /Home, prerendered/);

  // A folder asked for without its slash is sent to it, as Pail does.
  const about = await get('/about');
  assert.equal(about.status, 301);
  assert.equal(about.headers.get('location'), '/about/');
  assert.match(await (await get('/about/')).text(), /About, prerendered/);
  assert.match(await (await get('/blog/')).text(), /Blog, prerendered/);
  assert.equal(await (await get('/robots.txt')).text(), 'hello\n');

  assert.equal((await get('/about/', { method: 'POST' })).status, 405);
});

test('a page rendered on demand is answered by the function', async () => {
  const page = await get('/blog/hello?x=1', { headers: { 'X-Forwarded-Proto': 'https' } });
  assert.equal(page.status, 200);
  assert.match(page.headers.get('content-type'), /^text\/html/);
  assert.deepEqual(page.headers.getSetCookie(), ['seen=hello', 'theme=night']);
  const html = await page.text();
  assert.match(html, /<h1>Post hello<\/h1>/);
  // The page sees the address the visitor used, and who they are.
  assert.ok(html.includes(`<p id="url">https://127.0.0.1:${server.address().port}/blog/hello?x=1</p>`), html);
  assert.match(html, /<p id="ip">127\.0\.0\.1<\/p>/);
  // What the page printed went to the pail's output, not into the response.
  assert.ok(!html.includes('logging to standard output'));
  assert.ok(output.includes('a page logging to standard output'));
});

test('an API route gets the query, the headers and pail.json’s env', async () => {
  const res = await get('/api/echo?q=hi', { headers: { 'User-Agent': 'pail-test' } });
  assert.equal(res.headers.get('content-type'), 'application/json');
  assert.deepEqual(await res.json(), { q: 'hi', agent: 'pail-test', secret: 'from pail.json' });
});

test('a body goes in and comes back byte for byte', async () => {
  const bytes = new Uint8Array(70_000).map((_, i) => i % 256);
  const res = await get('/api/echo', {
    method: 'POST',
    body: bytes,
    headers: { 'Content-Type': 'application/x-thing' },
  });
  assert.equal(res.status, 201);
  assert.equal(res.headers.get('content-type'), 'application/x-thing');
  assert.equal(res.headers.get('x-bytes'), '70000');
  assert.deepEqual(new Uint8Array(await res.arrayBuffer()), bytes);
});

test('a form posted from the site’s own pages is let through, and can redirect', async () => {
  const form = { method: 'POST', body: new URLSearchParams({ slug: 'new' }) };
  const res = await get('/api/form', { ...form, headers: { Origin: origin } });
  assert.equal(res.status, 303);
  assert.equal(res.headers.get('location'), '/blog/new');
  // From anywhere else, Astro's own check refuses it.
  assert.equal((await get('/api/form', { ...form, headers: { Origin: 'https://elsewhere.example' } })).status, 403);
});

test('paths with nothing at them get the site’s own 404 page', async () => {
  // No route of the site's matches.
  const nowhere = await get('/nowhere');
  assert.equal(nowhere.status, 404);
  assert.match(await nowhere.text(), /Nothing here, prerendered/);
  // A page rendered on demand says so itself.
  const missing = await get('/blog/missing');
  assert.equal(missing.status, 404);
  assert.match(await missing.text(), /Nothing here, prerendered/);
});

test('HEAD gets the headers and no body', async () => {
  const res = await get('/blog/hello', { method: 'HEAD' });
  assert.equal(res.status, 200);
  assert.equal(await res.text(), '');
});

test('a redirect in the config is answered by the function', async () => {
  const res = await get('/old');
  assert.equal(res.status, 301);
  assert.equal(res.headers.get('location'), '/about');
});

test('a page that throws is a 500, and says why in the output', async () => {
  const res = await get('/api/broken');
  assert.equal(res.status, 500);
  assert.ok(
    output.some((line) => line.includes('this route is broken')),
    output.join('\n'),
  );
});

test('a site with a base is served under it', async () => {
  await build({ root, logLevel: 'error', base: '/docs', outDir: './dist-base' });
  const pail = JSON.parse(await readFile(`${root}dist-base/pail.json`, 'utf8'));
  // Every file is under the base, and so is everything rendered on demand:
  // the function reaches no other folder.
  assert.deepEqual(pail.routes, [
    { path: '/docs/404', to: 'static' },
    { path: '/docs', to: 'static' },
    { path: '/docs/robots.txt', to: 'static' },
    { path: '/docs/about/*', to: 'static' },
    { path: '/docs/blog', to: 'static' },
    { path: '/docs/styled/*', to: 'static' },
    { path: '/*', to: 'function:ssr' },
  ]);

  const answer = await createPail(`${root}dist-base`);
  const based = createServer((req, res) => answer(req, res));
  await new Promise((resolve) => based.listen(0, '127.0.0.1', resolve));
  try {
    const at = (path) => fetch(`http://127.0.0.1:${based.address().port}${path}`, { redirect: 'manual' });
    assert.match(await (await at('/docs/')).text(), /Home, prerendered/);
    assert.match(await (await at('/docs/about/')).text(), /About, prerendered/);
    assert.match(await (await at('/docs/blog/hello')).text(), /<h1>Post hello<\/h1>/);
    const nowhere = await at('/docs/nowhere');
    assert.equal(nowhere.status, 404);
    assert.match(await nowhere.text(), /Nothing here, prerendered/);
  } finally {
    based.close();
  }
});

test('a site that prerenders by default still gets a function for the pages that don’t', async () => {
  await build({ root, logLevel: 'error', output: 'static', outDir: './dist-hybrid' });
  const pail = JSON.parse(await readFile(`${root}dist-hybrid/pail.json`, 'utf8'));
  assert.deepEqual(Object.keys(pail.functions), ['ssr']);
  assert.deepEqual(pail.routes.at(-1), { path: '/*', to: 'function:ssr' });
});

test('a site with nothing rendered on demand is built as files, with no pail.json', async () => {
  const only = fileURLToPath(new URL('./fixtures/static/', import.meta.url));
  await build({ root: only, logLevel: 'error' });
  assert.match(await readFile(`${only}dist/index.html`, 'utf8'), /Only files/);
  await assert.rejects(access(`${only}dist/pail.json`));
  await assert.rejects(access(`${only}dist/server`));
});

test('a site with no 404 page of its own still hands whole folders to files', async () => {
  // Astro supplies a 404 page, rendered on demand, when the site has none.
  const plain = fileURLToPath(new URL('./fixtures/no-404/', import.meta.url));
  await build({ root: plain, logLevel: 'error' });
  const pail = JSON.parse(await readFile(`${plain}dist/pail.json`, 'utf8'));
  assert.deepEqual(pail.routes, [
    { path: '/', to: 'static' },
    { path: '/guide/*', to: 'static' },
    { path: '/*', to: 'function:ssr' },
  ]);
});
