// A Pail function speaks CGI: the request arrives as environment variables
// and standard input, and the response leaves on standard output as header
// lines, a blank line, then the body. These turn one into a Request and a
// Response into the other.

/**
 * The request CGI's variables describe.
 * @param {Record<string, string | undefined>} env
 * @param {Uint8Array} [body]
 */
export function requestFromCGI(env, body) {
  const method = (env.REQUEST_METHOD || 'GET').toUpperCase();
  const headers = new Headers();
  for (const [key, value] of Object.entries(env)) {
    if (!key.startsWith('HTTP_') || value === undefined) continue;
    setHeader(headers, key.slice(5).toLowerCase().replaceAll('_', '-'), value);
  }
  if (env.CONTENT_TYPE) setHeader(headers, 'content-type', env.CONTENT_TYPE);

  const hasBody = method !== 'GET' && method !== 'HEAD' && body !== undefined && body.byteLength > 0;
  if (hasBody) headers.set('content-length', String(body.byteLength));
  return new Request(urlFromCGI(env), { method, headers, body: hasBody ? body : undefined });
}

/**
 * The address the visitor asked for.
 * @param {Record<string, string | undefined>} env
 */
export function urlFromCGI(env) {
  // Pail says https when it terminated TLS itself. Behind a proxy that did,
  // the proxy says so, and Pail takes its word the same way.
  const forwarded = (env.HTTP_X_FORWARDED_PROTO || '').split(',')[0].trim().toLowerCase();
  const scheme = forwarded === 'https' || env.HTTPS === 'on' ? 'https' : env.REQUEST_SCHEME || 'http';
  const host = env.HTTP_HOST || [env.SERVER_NAME || 'localhost', env.SERVER_PORT].filter(Boolean).join(':');
  const query = env.QUERY_STRING ? `?${env.QUERY_STRING}` : '';
  return new URL(`${scheme}://${host}${env.REQUEST_URI || (env.PATH_INFO || '/') + query}`);
}

/** A header the runtime refuses, such as one with a control character, is left out. */
function setHeader(headers, name, value) {
  try {
    headers.set(name, value);
  } catch {}
}

// Pail works these out from the body it is handed.
const unsent = new Set([
  'connection',
  'content-length',
  'keep-alive',
  'set-cookie',
  'status',
  'transfer-encoding',
  'upgrade',
]);

/**
 * A response as CGI writes it.
 * @param {Response} response
 */
export async function responseToCGI(response) {
  const lines = [`Status: ${response.status}`];
  for (const [name, value] of response.headers) {
    if (!unsent.has(name)) lines.push(`${name}: ${value}`);
  }
  // Each cookie gets a line of its own: joined with commas, they can't be
  // told apart again.
  for (const cookie of response.headers.getSetCookie()) lines.push(`set-cookie: ${cookie}`);
  const head = Buffer.from(`${lines.join('\r\n')}\r\n\r\n`, 'utf8');
  return Buffer.concat([head, Buffer.from(await response.arrayBuffer())]);
}

/**
 * What a program wrote, as the response it describes. This is the other side
 * of responseToCGI: what Pail does with a function's output.
 * @param {Buffer} out
 */
export function parseCGI(out) {
  let end = out.indexOf('\r\n\r\n');
  let gap = 4;
  const bare = out.indexOf('\n\n');
  if (end < 0 || (bare >= 0 && bare < end)) [end, gap] = [bare, 2];
  if (end < 0) throw new Error("its output doesn't start with header lines");

  let status = 0;
  /** @type {[string, string][]} */
  const headers = [];
  for (const line of out.subarray(0, end).toString('utf8').split(/\r?\n/)) {
    const colon = line.indexOf(':');
    if (colon < 1) throw new Error("its output doesn't start with header lines");
    const name = line.slice(0, colon).trim().toLowerCase();
    const value = line.slice(colon + 1).trim();
    if (name === 'status') status = Number.parseInt(value, 10);
    else if (!unsent.has(name) || name === 'set-cookie') headers.push([name, value]);
  }
  if (!status) status = headers.some(([name]) => name === 'location') ? 302 : 200;
  if (!(status >= 200 && status <= 599)) throw new Error("its Status header isn't a status, like 404");
  if (!headers.some(([name]) => name === 'content-type')) headers.push(['content-type', 'text/plain; charset=utf-8']);
  return { status, headers, body: out.subarray(end + gap) };
}
