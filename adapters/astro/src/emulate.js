// Pail, as far as a build's output can tell: the routes of its pail.json,
// files served the way Pail serves them, and the function run once for each
// request. astro preview answers with this, so what works here works there.

import { spawn } from 'node:child_process';
import { createReadStream } from 'node:fs';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { parseCGI } from './cgi.js';

/**
 * A request handler that answers as Pail would with the build in dir.
 * @param {string} dir the folder pail.json is in: the build's outDir
 * @param {{ log?: (line: string) => void }} [options] log receives what a
 *   function prints to standard error, a line at a time
 * @returns {Promise<(req: import('node:http').IncomingMessage, res: import('node:http').ServerResponse) => Promise<void>>}
 */
export async function createPail(dir, { log = () => {} } = {}) {
  // Without a pail.json, the build is files and nothing else.
  let config = { static: '.', functions: {}, routes: [] };
  try {
    config = { functions: {}, routes: [], ...JSON.parse(await readFile(path.join(dir, 'pail.json'), 'utf8')) };
  } catch (err) {
    if (err.code !== 'ENOENT') throw err;
  }
  const root = config.static ? path.join(dir, config.static) : undefined;

  return async (req, res) => {
    const url = new URL(req.url, 'http://pail');
    let pathname;
    try {
      pathname = path.posix.normalize(decodeURIComponent(url.pathname));
    } catch {
      return plain(res, 400, 'That address can’t be read.');
    }
    if (pathname.length > 1) pathname = pathname.replace(/\/$/, '');

    const route = config.routes.length > 0 ? config.routes.find((r) => routeFits(r.path, pathname)) : { to: 'static' };
    if (!route) return plain(res, 404, `Nothing at ${url.pathname}.`);
    const [kind, name] = route.to.split(':');
    if (kind === 'function') return serveFunction(req, res, name, config.functions[name], dir, log);
    if (kind !== 'static' || !root) return plain(res, 502, `astro preview can’t answer for ${route.to}.`);
    return serveFile(req, res, root, pathname, url);
  };
}

/** /api/* covers /api and everything under it; a path with no star covers only itself. */
export function routeFits(pattern, pathname) {
  if (!pattern.endsWith('*')) return pathname === pattern || pathname === `${pattern}/`;
  const prefix = pattern.slice(0, -1);
  return pathname.startsWith(prefix) || pathname === prefix.replace(/\/$/, '');
}

async function serveFile(req, res, root, pathname, url) {
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    res.setHeader('Allow', 'GET, HEAD');
    return plain(res, 405, 'This path only serves files.');
  }
  const p = pathname.slice(1);
  const has = async (file) => (await stat(path.join(root, file)).catch(() => undefined))?.isFile() ?? false;

  let file;
  let status = 200;
  if (p === '' || url.pathname.endsWith('/')) {
    if (await has(path.posix.join(p, 'index.html'))) file = path.posix.join(p, 'index.html');
  } else if (await has(p)) {
    file = p;
  } else if (await has(`${p}/index.html`)) {
    // A folder asked for without its slash: relative links inside need it.
    res.writeHead(301, { Location: `/${p}/${url.search}` });
    return void res.end();
  } else if (await has(`${p}.html`)) {
    file = `${p}.html`;
  }
  if (!file) {
    if (!(await has('404.html'))) return plain(res, 404, `Nothing at ${url.pathname}.`);
    [file, status] = ['404.html', 404];
  }

  const { size } = await stat(path.join(root, file));
  res.writeHead(status, { 'Content-Type': contentType(file), 'Content-Length': size, 'Cache-Control': 'no-cache' });
  if (req.method === 'HEAD') return void res.end();
  createReadStream(path.join(root, file)).pipe(res);
}

async function serveFunction(req, res, name, fn, dir, log) {
  if (!fn) return plain(res, 500, `pail.json declares no function called ${name}.`);
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const body = Buffer.concat(chunks);

  const argv = typeof fn.cmd === 'string' ? ['sh', '-c', fn.cmd] : fn.cmd;
  const timeout = duration(fn.timeout, 10_000);
  const child = spawn(argv[0], argv.slice(1), {
    cwd: path.join(dir, fn.src),
    env: {
      PATH: process.env.PATH,
      NODE_ENV: 'production',
      HOME: '/tmp',
      TMPDIR: '/tmp',
      ...fn.env,
      PAIL_NAME: 'preview',
      PAIL_DEPLOY: 'preview',
      ...cgiEnv(req, body.length),
    },
    timeout,
    killSignal: 'SIGKILL',
  });
  const out = [];
  let rest = '';
  child.stdout.on('data', (chunk) => out.push(chunk));
  child.stderr.on('data', (chunk) => {
    const lines = (rest + chunk).split('\n');
    rest = lines.pop();
    for (const line of lines) log(line);
  });
  child.stdin.on('error', () => {}); // a program needn't read its input
  child.stdin.end(body);
  const [code, signal] = await new Promise((resolve) => {
    child.on('error', () => resolve([127, null]));
    child.on('close', (...how) => resolve(how));
  });
  if (rest) log(rest);

  let why = '';
  if (signal === 'SIGKILL') why = `${name} ran longer than its timeout of ${timeout / 1000}s and was stopped.`;
  else if (signal) why = `${name} was stopped by ${signal}.`;
  else if (code !== 0) why = `${name} exited with status ${code}.`;
  if (why) {
    log(why);
    return plain(res, 500, why);
  }

  try {
    const { status, headers, body: content } = parseCGI(Buffer.concat(out));
    for (const [key, value] of headers) res.appendHeader(key, value);
    res.setHeader('Content-Length', content.length);
    res.writeHead(status);
    res.end(req.method === 'HEAD' ? undefined : content);
  } catch (err) {
    log(`${name} answered, but not as CGI: ${err.message}.`);
    plain(res, 500, `${name} answered in a way Pail couldn’t read.`);
  }
}

/** A request as CGI's environment variables, the ones Pail sets. */
function cgiEnv(req, length) {
  const url = new URL(req.url, 'http://pail');
  const [host, port = '80'] = (req.headers.host ?? 'localhost').split(':');
  const env = {
    GATEWAY_INTERFACE: 'CGI/1.1',
    SERVER_SOFTWARE: 'pail',
    SERVER_PROTOCOL: `HTTP/${req.httpVersion}`,
    SERVER_NAME: host,
    SERVER_PORT: port,
    REQUEST_SCHEME: 'http',
    REQUEST_METHOD: req.method,
    SCRIPT_NAME: '',
    PATH_INFO: url.pathname,
    QUERY_STRING: url.search.slice(1),
    REQUEST_URI: req.url,
    REMOTE_ADDR: req.socket.remoteAddress ?? '',
    CONTENT_LENGTH: String(length),
    CONTENT_TYPE: req.headers['content-type'] ?? '',
  };
  for (const [key, value] of Object.entries(req.headers)) {
    if (key === 'content-type' || key === 'content-length' || key === 'proxy') continue;
    const joined = Array.isArray(value) ? value.join(key === 'cookie' ? '; ' : ', ') : value;
    env[`HTTP_${key.toUpperCase().replaceAll('-', '_')}`] = joined;
  }
  return env;
}

/** A length of time from pail.json, in milliseconds: "10s", "5m", or a bare number of seconds. */
function duration(value, fallback) {
  if (typeof value === 'number') return value * 1000;
  const units = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  let total = 0;
  for (const [, n, unit] of String(value ?? '').matchAll(/([\d.]+)\s*(ms|s|m|h)/g)) total += Number(n) * units[unit];
  return total || fallback;
}

function plain(res, status, text) {
  res.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
  res.end(`${text}\n`);
}

const types = {
  '.avif': 'image/avif',
  '.css': 'text/css; charset=utf-8',
  '.gif': 'image/gif',
  '.html': 'text/html; charset=utf-8',
  '.ico': 'image/x-icon',
  '.jpeg': 'image/jpeg',
  '.jpg': 'image/jpeg',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json',
  '.map': 'application/json',
  '.mjs': 'text/javascript; charset=utf-8',
  '.mp4': 'video/mp4',
  '.pdf': 'application/pdf',
  '.png': 'image/png',
  '.svg': 'image/svg+xml',
  '.txt': 'text/plain; charset=utf-8',
  '.wasm': 'application/wasm',
  '.webmanifest': 'application/manifest+json',
  '.webp': 'image/webp',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.xml': 'application/xml',
};

const contentType = (file) => types[path.extname(file).toLowerCase()] ?? 'application/octet-stream';
