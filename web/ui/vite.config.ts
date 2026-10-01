import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The build lands where pail-server embeds it. In dev, the API is a Pail
// server on :8080 (PAIL_DEV_API points somewhere else). The proxy sends the
// server's own host, so the pail URLs it answers with carry its port, not
// this one's.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    // The design system sits beside this project, not inside it.
    fs: { allow: ['..'] },
    proxy: { '/api': { target: process.env.PAIL_DEV_API ?? 'http://localhost:8080', changeOrigin: true } },
  },
});
