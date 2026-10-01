import { defineConfig } from 'astro/config';
import pail from '../../../src/index.js';

export default defineConfig({
  output: 'server',
  adapter: pail({ timeout: '20s', env: { SECRET: 'from pail.json' } }),
  redirects: { '/old': '/about' },
});
