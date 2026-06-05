import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

// Builds the UI into dist/ for go:embed. Asset filenames are content-hashed
// (Vite's default), so the Go server serves everything under /assets/ as
// immutable and a redeploy busts caches by URL — a new build means new
// filenames, so a stale asset is never requested (see uiHandler). The built
// index.html carries the current hashes and is gitignored to avoid churn; only
// the static favicon (copied from public/) is committed, anchoring the embed on
// a clean checkout.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
