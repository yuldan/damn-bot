// Cache-only service worker (GAP-UX-10): static assets are cached; session
// data, API responses and streams are never stored.
const CACHE = "damnbot-assets-v1";
const ASSETS = [
  "/assets/app.css",
  "/assets/app.js",
  "/assets/settings.js",
  "/assets/login.js",
  "/icon-192.png",
  "/icon-512.png",
  "/manifest.webmanifest",
];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(ASSETS)));
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)))
    )
  );
  self.clients.claim();
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (!url.pathname.startsWith("/assets/") && url.pathname !== "/manifest.webmanifest") return;
  event.respondWith(
    caches.match(request).then((hit) =>
      hit ??
      fetch(request).then((response) => {
        const copy = response.clone();
        caches.open(CACHE).then((cache) => cache.put(request, copy));
        return response;
      })
    )
  );
});
