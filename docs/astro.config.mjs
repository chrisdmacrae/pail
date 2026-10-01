import { defineConfig } from 'astro/config';
import pail from 'astro-pail';

export default defineConfig({
  // The docs are deployed to a pail. Every page is prerendered and served as
  // a file. Search is the one route rendered on demand, src/pages/api/search.ts,
  // which the adapter builds into a function.
  adapter: pail(),
  // The design system sets code in one ink on a sunk surface; no syntax colours.
  markdown: { syntaxHighlight: false },
  vite: {
    // The design system sits in web/, beside this project.
    server: { fs: { allow: ['..'] } },
  },
});
