import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The question picture page (card.html, src/card): built apart from the
// panel into dist/card, with every asset (fonts, the logo) inlined, so the
// API can put the page into a headless browser whole (web/admin/embed.go).
export default defineConfig({
  plugins: [react()],
  publicDir: false,
  build: {
    outDir: 'dist/card',
    emptyOutDir: true,
    sourcemap: false,
    assetsInlineLimit: () => true,
    cssCodeSplit: false,
    rollupOptions: { input: 'card.html' },
  },
});
