import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { _resetForTest, captureFromURL } from '@/lib/auth';
import { api } from './client';

describe('api.adminSyncLibraries', () => {
  beforeEach(() => {
    _resetForTest();
    sessionStorage.clear();
    captureFromURL(new URLSearchParams('?token=sync-token'));
    window.history.replaceState({}, '', '/api/v1/plugins/11/admin');
  });

  it('posts the selected backend installation id with auth and returns stats', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ created: 1, updated: 2, pruned: 3, kept: 4 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(api.adminSyncLibraries('29')).resolves.toEqual({
      created: 1,
      updated: 2,
      pruned: 3,
      kept: 4,
    });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/plugins/11/api/v1/admin/libraries/sync?backend_plugin_id=29',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({
          Authorization: 'Bearer sync-token',
        }),
      }),
    );
  });
});

describe('fetchInstalledBackends', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
    sessionStorage.clear();
    _resetForTest();
    captureFromURL(new URLSearchParams('?token=backend-token'));
    window.history.replaceState({}, '', '/api/v1/plugins/11/admin');
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  function authHeaderAt(
    fetchMock: ReturnType<typeof vi.fn<typeof fetch>>,
    callIndex: number,
  ): string | null {
    const [, init] = fetchMock.mock.calls[callIndex] ?? [];
    return new Headers(init?.headers).get('Authorization');
  }

  it('filters audiobook library sources and request providers separately', async () => {
    const body = JSON.stringify([
      {
        id: 38,
        plugin_id: 'continuum.local-audiobooks',
        enabled: true,
        capabilities: [
          {
            type: 'audiobook_backend.v1',
            id: 'local_audiobooks',
            display_name: 'Local Audiobooks Library',
            description: 'Local source',
            metadata: {
              audiobook_roles: ['library_source'],
              supports_catalog: true,
            },
          },
        ],
        audiobook_roles: ['library_source'],
      },
      {
        id: 40,
        plugin_id: 'continuum.audiobook-requests',
        enabled: true,
        capabilities: [
          {
            type: 'audiobook_backend.v1',
            id: 'default',
            display_name: 'Audiobook Requests',
            description: 'Request provider',
            metadata: {
              audiobook_roles: ['request_provider'],
              supports_catalog: false,
            },
          },
        ],
        audiobook_roles: ['request_provider'],
      },
      {
        id: 47,
        plugin_id: 'continuum.bookwarehouse-audio',
        enabled: true,
        capabilities: [
          {
            type: 'audiobook_backend.v1',
            id: 'default',
            display_name: 'Book Warehouse Audio',
            description: 'Catalog backend',
            metadata: {
              audiobook_roles: ['library_source'],
              supports_catalog: true,
            },
          },
        ],
        audiobook_roles: ['library_source'],
      },
    ]);
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(body, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(body, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    globalThis.fetch = fetchMock;

    const { fetchInstalledBackends, fetchRequestProviders } = await import('./client');
    const backends = await fetchInstalledBackends();
    const providers = await fetchRequestProviders();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/admin/plugins/installations',
      expect.objectContaining({
        credentials: 'include',
      }),
    );
    expect(authHeaderAt(fetchMock, 0)).toBe('Bearer backend-token');
    expect(authHeaderAt(fetchMock, 1)).toBe('Bearer backend-token');
    expect(backends.map((item) => item.plugin_id)).toEqual([
      'continuum.local-audiobooks',
      'continuum.bookwarehouse-audio',
    ]);
    expect(providers.map((item) => item.plugin_id)).toEqual([
      'continuum.audiobook-requests',
    ]);
  });

  it('throws the real installed-backends error instead of silently returning an empty list', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response('<html>401</html>', {
        status: 401,
        headers: { 'Content-Type': 'text/html' },
      }),
    );
    globalThis.fetch = fetchMock;

    const { fetchInstalledBackends } = await import('./client');

    await expect(fetchInstalledBackends()).rejects.toThrow(
      /Could not load installed backends \(HTTP 401\)/,
    );
  });

  it('refreshes and retries host backend discovery after a 401', async () => {
    window.localStorage.setItem('refresh_token', 'refresh-token-1');

    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response('<html>401</html>', {
          status: 401,
          headers: { 'Content-Type': 'text/html' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            access_token: 'fresh-access-token',
            refresh_token: 'refresh-token-2',
          }),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([
            {
              id: 38,
              plugin_id: 'continuum.local-audiobooks',
              enabled: true,
              capabilities: [
                {
                  type: 'audiobook_backend.v1',
                  id: 'local_audiobooks',
                  display_name: 'Local Audiobooks Library',
                  metadata: {
                    audiobook_roles: ['library_source'],
                    supports_catalog: true,
                  },
                },
              ],
            },
          ]),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      );
    vi.stubGlobal('fetch', fetchMock);

    const { fetchInstalledBackends } = await import('./client');
    const backends = await fetchInstalledBackends();

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/admin/plugins/installations',
      expect.objectContaining({
        credentials: 'include',
      }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: 'refresh-token-1' }),
      credentials: 'include',
    });
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/admin/plugins/installations',
      expect.objectContaining({
        credentials: 'include',
      }),
    );
    expect(authHeaderAt(fetchMock, 0)).toBe('Bearer backend-token');
    expect(authHeaderAt(fetchMock, 2)).toBe('Bearer fresh-access-token');
    expect(window.localStorage.getItem('refresh_token')).toBe('refresh-token-2');
    expect(backends).toEqual([
      expect.objectContaining({
        id: 38,
        plugin_id: 'continuum.local-audiobooks',
        audiobook_roles: ['library_source'],
      }),
    ]);
  });
});
