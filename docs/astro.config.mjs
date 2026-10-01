import { defineConfig } from 'astro/config';

export default defineConfig({
  // The design system sets code in one ink on a sunk surface; no syntax colours.
  markdown: { syntaxHighlight: false },
  vite: {
    // The design system sits in web/, beside this project.
    server: { fs: { allow: ['..'] } },
  },
});
