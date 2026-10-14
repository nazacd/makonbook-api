import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The built panel goes to dist/app, which the API embeds (embed.go) and
// serves on the admin host. In development, `npm run dev` proxies the API
// so page and API share one origin, as in production.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist/app',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': { target: 'http://localhost:8080', changeOrigin: false },
    },
  },
});
