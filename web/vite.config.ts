import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

const CSP =
  "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'";

// The gallery build has its own folder, so publishing dist can never ship the gallery or its mock data.
const gallery = process.env.VITE_GALLERY === '1';

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: gallery ? 'dist-gallery' : 'dist',
    assetsInlineLimit: 0,
    cssCodeSplit: false,
    modulePreload: { polyfill: false },
    sourcemap: false,
  },
  preview: { host: '127.0.0.1', port: 4173, strictPort: true, headers: { 'Content-Security-Policy': CSP } },
  // Dev only: the API allows Host 127.0.0.1:8790, which changeOrigin sends; nginx proxies /api in production.
  server: { host: '127.0.0.1', proxy: { '/api': { target: 'http://127.0.0.1:8790', changeOrigin: true } } },
  test: { environment: 'jsdom', include: ['src/**/*.test.{ts,tsx}'], css: false },
});
