// konflate service worker — minimal and data-safe.
//
// Purpose: make the app installable and keep the shell available offline,
// WITHOUT ever serving stale live data. The forge / PR data (the JSON API and
// the websocket) is never cached — it always hits the network — so an installed
// konflate is exactly as live as the browser tab. Only the static app shell is
// cached: content-hashed /assets/ are immutable (cache-first); everything else,
// including index.html, is network-first so an online user is always current
// and a redeploy is picked up immediately, with the cache only as an offline
// fallback.
const CACHE = 'konflate-shell-v1';

self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      // Drop any caches from a previous shell version.
      const keys = await caches.keys();
      await Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)));
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);

  // Only our own origin and GETs; never the live-data endpoints (the websocket
  // never reaches here, but guard the API so a render is never served stale).
  if (request.method !== 'GET' || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) return;

  // Immutable, content-hashed build assets → cache-first.
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.open(CACHE).then(async (cache) => {
        const hit = await cache.match(request);
        if (hit) return hit;
        const res = await fetch(request);
        if (res.ok) cache.put(request, res.clone());
        return res;
      }),
    );
    return;
  }

  // Navigations + other shell files (index.html, icons, manifest, favicon):
  // network-first, falling back to cache only when offline.
  event.respondWith(
    (async () => {
      const cache = await caches.open(CACHE);
      try {
        const res = await fetch(request);
        if (res.ok) cache.put(request, res.clone());
        return res;
      } catch (err) {
        const hit = await cache.match(request);
        if (hit) return hit;
        if (request.mode === 'navigate') {
          const shell = await cache.match('/');
          if (shell) return shell;
        }
        throw err;
      }
    })(),
  );
});
