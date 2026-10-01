import { defineConfig } from 'astro/config';
import pail from 'astro-pail';

export default defineConfig({
  // The docs are deployed to a pail. Every page is prerendered and served as
  // a file. Two routes are rendered on demand, the search and the picture a
  // link to a page unfurls with, both in src/pages/api. The adapter builds
  // them into a function.
  adapter: pail({
    // Satori reads its wasm from beside its own files, so it can't be built
    // into the function's. Pail installs it where the function runs.
    external: ['satori'],
  }),
  // The design system sets code in one ink on a sunk surface; no syntax colours.
  markdown: { syntaxHighlight: false },
  vite: {
    // The design system sits in web/, beside this project.
    server: { fs: { allow: ['..'] } },
  },
});
