import type { AstroIntegration } from 'astro';

export interface Options {
  /**
   * The function's name in pail.json, as it shows on the pail's page and in
   * its output.
   * @default "ssr"
   */
  name?: string;
  /**
   * How much memory each copy of the function gets, like "512MB".
   * @default "256MB"
   */
  memory?: string;
  /**
   * How long one request may take, like "30s". Pail's own default is 10s.
   */
  timeout?: string;
  /**
   * How long a copy waits for another request before it sleeps, like "15m".
   * Pail's own default is 5m.
   */
  idle?: string;
  /**
   * How many copies may run at once. Each handles one request at a time.
   * Pail's own default is 4.
   */
  max?: number;
  /**
   * Environment variables for the function. They are written into pail.json,
   * which is stored with the deploy, though never served.
   */
  env?: Record<string, string>;
  /**
   * Packages to leave out of the function's own files, for Pail to install
   * where it runs: ones with programs compiled for one kind of machine.
   * sharp is always left out.
   */
  external?: string[];
}

/**
 * Deploys an Astro site to Pail. Prerendered pages and assets are served as
 * files; pages rendered on demand, and API routes, are answered by a function.
 */
export default function pail(options?: Options): AstroIntegration;
