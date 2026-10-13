// Minimal service worker — network-only, no caching.
// Required by Chrome/Brave to enable PWA installation.
self.addEventListener('fetch', (e) => {
  e.respondWith(fetch(e.request));
});
