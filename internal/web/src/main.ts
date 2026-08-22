import { mount } from 'svelte';
import '@fontsource-variable/geist'; // UI/prose sans (--font-sans), self-hosted
import '@fontsource-variable/geist-mono'; // code/identifier mono (--font-mono)
import './app.css';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('#app mount point missing');

// Register the service worker in the built app only (the vite dev server has
// none): it makes konflate installable and keeps the shell available offline,
// without ever caching live data (see public/sw.js). Best-effort — a failure
// is silent and the app works fine without it.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    // Relative scope so the worker governs the base path konflate is served
    // under, whether that is the root or a subpath like /platform/konflate.
    void navigator.serviceWorker.register('./sw.js', { scope: './' }).catch(() => {});
  });
}

export default mount(App, { target });
