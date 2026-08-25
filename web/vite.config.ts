import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Daemon port the dev server proxies to; dev.sh / dev.ps1 export this so a
// custom -Port keeps the dev proxy coherent.
const daemonPort = process.env.WRONGTRACE_DAEMON_PORT ?? process.env.WRONGTRACE_PORT ?? '3445';
const vitePort = process.env.VITE_PORT ? parseInt(process.env.VITE_PORT, 10) : 3444;

// WrongTrace's Go daemon embeds web/dist at build time, so the dev server
// proxies API + WS calls to the local daemon (default :3445 in dev, :3444 in standalone).
export default defineConfig({
  plugins: [react()],
  server: {
    port: vitePort,
    proxy: {
      '/api': {
        target: `http://127.0.0.1:${daemonPort}`,
        changeOrigin: true,
        ws: true,
      },
      '/proxy': {
        target: `http://127.0.0.1:${daemonPort}`,
        changeOrigin: true,
      },
      '/v1': {
        target: `http://127.0.0.1:${daemonPort}`,
        changeOrigin: true,
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
			  // React Flow is only needed by the lazy-loaded Code Atlas tab.
			  // Keeping it out of the catch-all vendor group prevents an eager
			  // modulepreload on every dashboard visit.
			  name: 'vendor-atlas',
			  test: /[\\/]node_modules[\\/]@xyflow[\\/]/,
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
