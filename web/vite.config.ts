import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// WrongTrace's Go daemon embeds web/dist at build time, so the dev server
// proxies API + WS calls to the local daemon (default :4318). In production
// the dashboard is served by the Go binary itself, but the build artifact is
// identical.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:4318',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
});
