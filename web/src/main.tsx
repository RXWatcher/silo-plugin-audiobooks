import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import App from './App';
import './index.css';
import { mountPath } from './lib/mountPath';
import { captureFromURL, getCachedTheme } from './lib/auth';
import { captureProfileFromURL } from './lib/profile';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

const params = new URLSearchParams(window.location.search);
captureFromURL(params);
captureProfileFromURL();

// Strip ?token= from the URL so it doesn't show in history.
if (params.has('token')) {
  params.delete('token');
  const cleaned = params.toString();
  const url = window.location.pathname + (cleaned ? `?${cleaned}` : '') + window.location.hash;
  window.history.replaceState(null, '', url);
}

const theme = getCachedTheme();
if (theme) {
  document.documentElement.dataset.theme = theme;
}

// SPA is mounted at the plugin proxy root (no trailing /admin segment — this
// is a customer-facing portal, not an admin SPA).
const basename = mountPath() || '/';

// Register the service worker so the browser will offer to install the portal
// as a PWA. The SW lives at <mountPath>/sw.js and its scope is <mountPath>/
// — anything more aggressive would conflict with other plugins served from
// the same silo origin. We register from window load so the initial
// paint isn't delayed; HTTPS is required (silo enforces it via the
// public URL), so we just check for the API and let registration fail
// silently in dev / non-secure contexts.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    const scope = basename.endsWith('/') ? basename : basename + '/';
    const swURL = scope + 'sw.js';
    void navigator.serviceWorker.register(swURL, { scope }).catch(() => {
      // First-load registration can fail in development (no HTTPS) or when
      // the plugin proxy returns 401 for /sw.js. Either way the SPA still
      // works; we just don't get the PWA install prompt.
    });
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={basename}>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
