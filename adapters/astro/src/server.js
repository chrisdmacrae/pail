// The program Pail runs for each request to an on-demand page or an API
// route. It is built into the function's entry.mjs.

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { createApp } from 'astro/app/entrypoint';
import { setGetEnv } from 'astro/env/setup';
import { requestFromCGI, responseToCGI } from './cgi.js';
// First, so that it has run before any of the site's own code is loaded.
import { respond } from './stdout.js';

setGetEnv((key) => process.env[key]);

// Pail sends the response once the program has finished, so there is nobody
// to stream to.
const app = createApp({ streaming: false });

/**
 * Answers one request.
 * @param {Record<string, string | undefined>} env the request, as CGI's variables
 * @param {Uint8Array} [body]
 */
export async function handle(env, body) {
  let request;
  try {
    request = requestFromCGI(env, body);
  } catch {
    return new Response('Bad Request', { status: 400 });
  }
  try {
    // A prerendered page is a file, and Pail has already served it. A
    // redirect to one is still the function's to answer.
    let routeData = app.match(request, true);
    if (routeData?.prerender && routeData.type !== 'redirect') routeData = app.match(request);
    return await app.render(request, {
      routeData,
      addCookieHeader: true,
      clientAddress: env.REMOTE_ADDR || undefined,
      prerenderedErrorPageFetch,
    });
  } catch (err) {
    console.error(err);
    return new Response('Internal Server Error', { status: 500 });
  }
}

// A function can't ask its own pail for a page, so the prerendered 404 and
// 500 pages are kept beside the entry by the build.
async function prerenderedErrorPageFetch(url) {
  const status = /\/(404|500)(\.html|\/index\.html|\/)?$/.exec(new URL(url).pathname)?.[1];
  try {
    const page = await readFile(path.join(path.dirname(process.argv[1]), '_pail', `${status}.html`));
    return new Response(page, { headers: { 'Content-Type': 'text/html; charset=utf-8' } });
  } catch {
    return new Response(null, { status: 404 });
  }
}

async function stdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks);
}

// Run by Pail, this is one request: answer it and finish. Nothing waits for
// what the site left open, a database connection or a timer.
if (process.env.GATEWAY_INTERFACE) {
  const body = Number(process.env.CONTENT_LENGTH) > 0 ? await stdin() : undefined;
  const response = await handle(process.env, body);
  respond(await responseToCGI(response), () => process.exit(0));
}
