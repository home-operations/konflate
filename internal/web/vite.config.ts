import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

// Builds the UI into dist/ for go:embed. Asset names are stable (no content
// hash) so the committed dist/index.html doesn't churn on every build and the
// Go binary builds from a clean checkout; the Go file server sets ETags, so
// cache-busting is handled by validation rather than filename hashing.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        entryFileNames: 'assets/app.js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name].[ext]',
      },
    },
  },
});
