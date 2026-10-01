import { defineConfig } from 'astro/config';
import pail from 'astro-pail';

export default defineConfig({
  // The docs are deployed to a pail. Every page is prerendered, so Pail
  // serves them as files; a page rendered on demand would get a function.
  adapter: pail(),
  // The design system sets code in one ink on a sunk surface; no syntax colours.
  markdown: { syntaxHighlight: false },
  vite: {
    // The design system sits in web/, beside this project.
    server: { fs: { allow: ['..'] } },
  },
});
