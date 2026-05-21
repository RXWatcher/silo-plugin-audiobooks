// Minimal service worker for installability. We intentionally do not cache
// API responses or media bytes — audiobook streams are signed, authenticated,
// and large. Caching them would either serve stale auth tokens or eat disk.
//
// The SW exists so the browser will offer "Install app" and let the portal
// run in standalone mode (separate window, lock-screen MediaSession controls).
// On every fetch we passthrough to the network. If we want offline shell
// caching later, gate it behind a separate VERSION constant + cache.put.

self.addEventListener("install", () => {
  // Skip waiting so a freshly-deployed SW activates on next page load
  // instead of waiting for every tab to close.
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  // Take control of open clients immediately so the new SW handles their
  // fetches without a reload.
  event.waitUntil(self.clients.claim());
});

self.addEventListener("fetch", () => {
  // Passthrough: let the browser handle the network request normally.
  // Defining the handler is what makes the SW "active" for install metrics.
});
