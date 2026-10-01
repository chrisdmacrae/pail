// Standard output is the response. Whatever else the site prints there, a
// console.log in a page or a library's notice, would be read as part of it, so
// it goes to standard error instead: the pail's output.

import { Console } from 'node:console';

const write = process.stdout.write.bind(process.stdout);

if (process.env.GATEWAY_INTERFACE) {
  process.stdout.write = process.stderr.write.bind(process.stderr);
  globalThis.console = new Console(process.stderr, process.stderr);
}

/**
 * Writes the response, the one thing that goes to standard output.
 * @param {Uint8Array} bytes
 * @param {() => void} done called once it is all written
 */
export function respond(bytes, done) {
  write(bytes, done);
}
