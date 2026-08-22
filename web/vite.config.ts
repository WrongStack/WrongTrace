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
    rollupOptions: {
      output: {
        // Split the vendor graph so no chunk trips the 500 kB warning:
        // recharts (+d3) dominates the bundle, react is a stable long-term
        // cache candidate, and the rest of node_modules is small enough to
        // share. Segment boundaries are exact so @tanstack/react-query does
        // not match the react chunk.
        manualChunks(id: string): string | undefined {
          if (!id.includes('node_modules')) return undefined;
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) {
            return 'vendor-react';
          }
          if (/[\\/]node_modules[\\/](recharts|d3|d3-[a-z0-9-]+|victory-vendor|react-smooth|internmap)[\\/]/.test(id)) {
            return 'vendor-charts';
          }
          return 'vendor';
        },
      },
    },
  },
});
