// astro preview: the build, answered the way Pail would answer it.

import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { createPail } from './emulate.js';

/**
 * @param {import('astro').PreviewServerParams} preview
 * @returns {Promise<import('astro').PreviewServer>}
 */
export default async function createPreviewServer(preview) {
  const { logger } = preview;
  const answer = await createPail(fileURLToPath(preview.outDir), { log: (line) => logger.info(line) });
  const server = createServer((req, res) => {
    for (const [name, value] of Object.entries(preview.headers ?? {})) {
      if (value) res.setHeader(name, value);
    }
    answer(req, res).catch((err) => {
      logger.error(String(err?.stack ?? err));
      if (!res.headersSent) res.writeHead(500);
      res.end();
    });
  });

  const host = preview.host ?? 'localhost';
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(preview.port, host, resolve);
  });
  logger.info(`Answering as Pail would at http://${host}:${preview.port}${preview.base}`);

  const closed = new Promise((resolve) => server.once('close', resolve));
  return {
    host,
    port: preview.port,
    closed: () => closed,
    stop: () =>
      new Promise((resolve) => {
        server.close(() => resolve());
        server.closeAllConnections();
      }),
  };
}
