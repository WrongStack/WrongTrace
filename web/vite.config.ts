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
    // Vite 8 bundles with Rolldown, where the function-form manualChunks is
    // gone. The replacement is rolldownOptions.output.codeSplitting (the
    // initial advancedChunks option was renamed to codeSplitting in rolldown;
    // advancedChunks still works but warns). Groups are matched in order
    // (first match wins), preserving the exact segment boundaries of the
    // previous manualChunks split so @tanstack/react-query still lands in the
    // plain vendor chunk, not the react chunk.
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'vendor-react',
              test: /[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/,
            },
            {
              name: 'vendor-charts',
              test: /[\\/]node_modules[\\/](recharts|d3|d3-[a-z0-9-]+|victory-vendor|react-smooth|internmap)[\\/]/,
            },
            {
              name: 'vendor',
              test: /[\\/]node_modules[\\/]/,
            },
          ],
        },
      },
    },
  },
});
