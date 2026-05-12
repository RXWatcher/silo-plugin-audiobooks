import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import App from './App';
import './index.css';
import { mountPath } from './lib/mountPath';
import { captureFromURL, getCachedTheme } from './lib/auth';

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

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={basename}>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
