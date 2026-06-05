import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// vitePreprocess enables <script lang="ts"> and modern CSS in components.
export default {
  preprocess: vitePreprocess(),
};
