import { mountPath } from '@/lib/mountPath';
import { getCachedToken, setCachedToken } from '@/lib/auth';
import type {
  ABSSession,
  ABSStandaloneOptInState,
  ABSToken,
  AudiobookDetail,
  AudiobookSummary,
  AuthorSummary,
  BackendConfig,
  Bookmark,
  Collection,
  CollectionItem,
  InstalledBackend,
  InstalledCapability,
  LibraryInfo,
  ListeningStats,
  NarratorSummary,
  PageEnvelope,
  PlaybackSession,
  Podcast,
  PodcastEpisode,
  Progress,
  Rating,
  SeriesSummary,
  UserRequest,
} from './types';

function apiBase(): string {
  return `${mountPath()}/api/v1`;
}

function authHeaders(): Record<string, string> {
  const t = getCachedToken();
  return t ? { Authorization: `Bearer ${t}` } : {};
}

let refreshPromise: Promise<string | null> | null = null;

// refreshAccessToken posts to the Continuum host's `/api/v1/auth/refresh`
// (NOT a plugin-mounted route — the host owns session lifetime). It's
// intentionally hardcoded without `mountPath()` because the SPA is served
// from the host origin and refresh lives at host root.
//
// Currently dead in production: this function reads `refresh_token` from
// localStorage, but nothing in this SPA ever writes it — the plugin proxy
// hands the SPA an `?token=` access token only, and refresh tokens live on
// the host SPA's session storage. When the access token expires, the user
// reloads via the host sidebar to get a fresh one. The code is kept here so
// the retry path is wired when the host eventually issues refresh tokens
// directly to plugin SPAs.
async function refreshAccessToken(): Promise<string | null> {
  if (refreshPromise) {
    return refreshPromise;
  }

  refreshPromise = (async () => {
    let refreshToken: string | null = null;
    try {
      refreshToken = window.localStorage.getItem('refresh_token');
    } catch {
      return null;
    }

    if (!refreshToken) {
      return null;
    }

    const response = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
      credentials: 'include',
    });
    if (!response.ok) {
      return null;
    }

    const data = await response.json();
    setCachedToken(data.access_token ?? null);
    if (data.refresh_token) {
      try {
        window.localStorage.setItem('refresh_token', data.refresh_token);
      } catch {
        // Storage may be unavailable; keep using the in-memory access token.
      }
    }
    return getCachedToken();
  })().finally(() => {
    refreshPromise = null;
  });

  return refreshPromise;
}

export async function authedFetch(input: string, init?: RequestInit): Promise<Response> {
  const headers = {
    ...(init?.headers as Record<string, string> | undefined),
    ...authHeaders(),
  };
  let res = await fetch(input, { ...init, headers, credentials: init?.credentials ?? 'include' });
  if (res.status !== 401) {
    return res;
  }

  const freshToken = await refreshAccessToken();
  if (!freshToken) {
    return res;
  }

  return fetch(input, {
    ...init,
    headers: {
      ...(init?.headers as Record<string, string> | undefined),
      Authorization: `Bearer ${freshToken}`,
    },
    credentials: init?.credentials ?? 'include',
  });
}

async function jsonOrThrow<T>(r: Response): Promise<T> {
  if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => '')}`);
  const data = await r.json();
  return normalizeListEnvelope(data) as T;
}

function normalizeListEnvelope(data: unknown): unknown {
  if (data && typeof data === 'object' && 'items' in data && (data as { items: unknown }).items == null) {
    return { ...data, items: [] };
  }
  return data;
}

async function noContentOrThrow(r: Response): Promise<void> {
  if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => '')}`);
}

export interface ListQuery {
  cursor?: string;
  limit?: number;
  sort?: string;
  order?: string;
  q?: string;
  library_id?: number;
}

function toQuery(p?: ListQuery): string {
  if (!p) return '';
  const q = new URLSearchParams();
  if (p.cursor) q.set('cursor', p.cursor);
  if (p.limit) q.set('limit', String(p.limit));
  if (p.sort) q.set('sort', p.sort);
  if (p.order) q.set('order', p.order);
  if (p.q) q.set('q', p.q);
  if (p.library_id) q.set('library_id', String(p.library_id));
  const enc = q.toString();
  return enc ? `?${enc}` : '';
}

export const api = {
  // Catalog
  listAudiobooks: (p?: ListQuery) =>
    authedFetch(`${apiBase()}/audiobooks${toQuery(p)}`).then(
      jsonOrThrow<PageEnvelope<AudiobookSummary>>,
    ),

  searchAudiobooks: (q: string, libraryID?: number) =>
    authedFetch(
      `${apiBase()}/audiobooks/search${toQuery({ q, library_id: libraryID })}`,
    ).then(
      jsonOrThrow<PageEnvelope<AudiobookSummary>>,
    ),

  listLibraries: () =>
    authedFetch(`${apiBase()}/libraries`).then(jsonOrThrow<{ items: LibraryInfo[] }>),

  getAudiobook: (id: string) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(id)}`).then(
      jsonOrThrow<{
        audiobook: AudiobookDetail;
        progress?: Progress;
        bookmarks?: Bookmark[];
        rating?: Rating;
      }>,
    ),

  browseAuthors: (p?: ListQuery) =>
    authedFetch(`${apiBase()}/browse/authors${toQuery(p)}`).then(
      jsonOrThrow<PageEnvelope<AuthorSummary>>,
    ),

  browseSeries: (p?: ListQuery) =>
    authedFetch(`${apiBase()}/browse/series${toQuery(p)}`).then(
      jsonOrThrow<PageEnvelope<SeriesSummary>>,
    ),

  browseNarrators: (p?: ListQuery) =>
    authedFetch(`${apiBase()}/browse/narrators${toQuery(p)}`).then(
      jsonOrThrow<PageEnvelope<NarratorSummary>>,
    ),

  // streamUrl was previously used by the player to construct an authenticated
  // URL client-side. With signed URLs, each AudiobookFile in the detail
  // response carries its own stream_url (portal-signed, short-TTL); the
  // player reads file.stream_url directly. This helper remains only for
  // back-compat with code paths that still want the portal redirect (e.g.
  // older mobile clients) — it does not embed any auth, so callers must
  // either authenticate via Bearer header or use file.stream_url instead.
  streamUrl: (bookId: string, fileIdx: number) =>
    `${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/files/${fileIdx}/stream`,

  // User state
  listMyProgress: (limit = 24) =>
    authedFetch(`${apiBase()}/me/progress?limit=${limit}`).then(
      jsonOrThrow<{ items: Progress[] }>,
    ),

  upsertProgress: (
    bookId: string,
    body: { current_seconds: number; progress_pct?: number; is_finished?: boolean },
  ) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/progress`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(noContentOrThrow),

  getListeningStats: (bookId: string) =>
    authedFetch(`${apiBase()}/me/listening-stats/${encodeURIComponent(bookId)}`).then(
      jsonOrThrow<ListeningStats>,
    ),

  listMyPlaybackSessions: () =>
    authedFetch(`${apiBase()}/me/playback-sessions`).then(
      jsonOrThrow<{ items: ABSSession[] }>,
    ),

  createPlaybackSession: (
    bookId: string,
    body: { current_seconds?: number; device_id?: string; device_info?: Record<string, unknown> },
  ) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/playback-session`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<PlaybackSession>),

  updatePlaybackSession: (
    sessionId: string,
    body: {
      current_seconds: number;
      duration_seconds?: number;
      progress_pct?: number;
      is_finished?: boolean;
      time_listened_seconds?: number;
    },
  ) =>
    authedFetch(`${apiBase()}/playback-sessions/${encodeURIComponent(sessionId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(noContentOrThrow),

  closePlaybackSession: (
    sessionId: string,
    body: {
      current_seconds: number;
      duration_seconds?: number;
      progress_pct?: number;
      is_finished?: boolean;
      time_listened_seconds?: number;
    },
  ) =>
    authedFetch(`${apiBase()}/playback-sessions/${encodeURIComponent(sessionId)}/close`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(noContentOrThrow),

  listBookmarks: (bookId: string) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/bookmarks`).then(
      jsonOrThrow<{ items: Bookmark[] }>,
    ),

  createBookmark: (
    bookId: string,
    body: { position_seconds: number; chapter_id?: string; note?: string },
  ) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/bookmarks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<Bookmark>),

  deleteBookmark: (bookId: string, bookmarkId: string) =>
    authedFetch(
      `${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/bookmarks/${encodeURIComponent(bookmarkId)}`,
      { method: 'DELETE' },
    ).then(noContentOrThrow),

  upsertRating: (bookId: string, rating: number) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/rating`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ rating }),
    }).then(noContentOrThrow),

  deleteRating: (bookId: string) =>
    authedFetch(`${apiBase()}/audiobooks/${encodeURIComponent(bookId)}/rating`, {
      method: 'DELETE',
    }).then(noContentOrThrow),

  // Requests
  listMyRequests: () =>
    authedFetch(`${apiBase()}/me/requests`).then(jsonOrThrow<{ items: UserRequest[] }>),

  createRequest: (body: { title: string; author?: string; isbn?: string }) =>
    authedFetch(`${apiBase()}/me/requests`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<{ request_id: string; status: string }>),

  cancelRequest: (id: string) =>
    authedFetch(`${apiBase()}/me/requests/${encodeURIComponent(id)}`, { method: 'DELETE' }).then(
      noContentOrThrow,
    ),

  // Collections
  listMyCollections: () =>
    authedFetch(`${apiBase()}/me/collections`).then(jsonOrThrow<{ items: Collection[] }>),

  listPublicCollections: () =>
    authedFetch(`${apiBase()}/collections/public`).then(jsonOrThrow<{ items: Collection[] }>),

  createCollection: (body: Partial<Collection> & { name: string }) =>
    authedFetch(`${apiBase()}/me/collections`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<Collection>),

  updateCollection: (id: string, body: Partial<Collection>) =>
    authedFetch(`${apiBase()}/me/collections/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(noContentOrThrow),

  deleteCollection: (id: string) =>
    authedFetch(`${apiBase()}/me/collections/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }).then(noContentOrThrow),

  listCollectionItems: (id: string) =>
    authedFetch(`${apiBase()}/me/collections/${encodeURIComponent(id)}/items`).then(
      jsonOrThrow<{ items: CollectionItem[] }>,
    ),

  addCollectionItem: (collectionId: string, bookId: string) =>
    authedFetch(`${apiBase()}/me/collections/${encodeURIComponent(collectionId)}/items`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ book_id: bookId }),
    }).then(noContentOrThrow),

  removeCollectionItem: (collectionId: string, bookId: string) =>
    authedFetch(
      `${apiBase()}/me/collections/${encodeURIComponent(collectionId)}/items/${encodeURIComponent(bookId)}`,
      { method: 'DELETE' },
    ).then(noContentOrThrow),

  // Podcasts — read endpoints (authenticated user).
  listPodcasts: (libraryID?: number) => {
    const q = libraryID ? `?library_id=${libraryID}` : '';
    return authedFetch(`${apiBase()}/podcasts${q}`).then(jsonOrThrow<{ items: Podcast[] }>);
  },

  getPodcast: (id: string) =>
    authedFetch(`${apiBase()}/podcasts/${encodeURIComponent(id)}`).then(jsonOrThrow<Podcast>),

  listPodcastEpisodes: (podcastID: string) =>
    authedFetch(
      `${apiBase()}/podcasts/${encodeURIComponent(podcastID)}/episodes`,
    ).then(jsonOrThrow<{ items: PodcastEpisode[] }>),

  // Admin endpoints (admin role gate enforced server-side).
  adminCreatePodcast: (body: Partial<Podcast> & { title: string; library_id: number }) =>
    authedFetch(`${apiBase()}/admin/podcasts`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<Podcast>),

  adminDeletePodcast: (id: string) =>
    authedFetch(`${apiBase()}/admin/podcasts/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }).then(noContentOrThrow),

  adminRefreshPodcast: (id: string) =>
    authedFetch(`${apiBase()}/admin/podcasts/${encodeURIComponent(id)}/refresh`, {
      method: 'POST',
    }).then(jsonOrThrow<Podcast>),

  // ABS standalone-port body-creds login opt-in (per user).
  getABSStandaloneOptIn: () =>
    authedFetch(`${apiBase()}/me/abs-standalone`).then(jsonOrThrow<ABSStandaloneOptInState>),

  enableABSStandaloneOptIn: () =>
    authedFetch(`${apiBase()}/me/abs-standalone`, { method: 'POST' }).then(
      jsonOrThrow<{ enabled: boolean }>,
    ),

  disableABSStandaloneOptIn: () =>
    authedFetch(`${apiBase()}/me/abs-standalone`, { method: 'DELETE' }).then(
      jsonOrThrow<{ enabled: boolean }>,
    ),

  // Admin
  getBackendConfig: () =>
    authedFetch(`${apiBase()}/admin/backend-config`).then(jsonOrThrow<BackendConfig>),

  updateBackendConfig: (body: Partial<BackendConfig> & { rotate_abs_secret?: boolean }) =>
    authedFetch(`${apiBase()}/admin/backend-config`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(jsonOrThrow<BackendConfig>),

  adminListLibraries: () =>
    authedFetch(`${apiBase()}/admin/libraries`).then(jsonOrThrow<{ items: LibraryInfo[] }>),

  adminReplaceLibraries: (items: LibraryInfo[]) =>
    authedFetch(`${apiBase()}/admin/libraries`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ items }),
    }).then(noContentOrThrow),

  adminBackendLibraries: (backendPluginID: string) =>
    authedFetch(
      `${apiBase()}/admin/backend-libraries?backend_plugin_id=${encodeURIComponent(backendPluginID)}`,
    ).then(jsonOrThrow<{ items: LibraryInfo[] }>),

  adminSyncLibraries: (backendPluginID: string) =>
    authedFetch(
      `${apiBase()}/admin/libraries/sync?backend_plugin_id=${encodeURIComponent(backendPluginID)}`,
      { method: 'POST' },
    ).then(
      jsonOrThrow<{
        created: number;
        updated: number;
        pruned: number;
        kept: number;
      }>,
    ),

  adminListRequests: (status: string) =>
    authedFetch(`${apiBase()}/admin/requests?status=${encodeURIComponent(status)}`).then(
      jsonOrThrow<{ items: UserRequest[] }>,
    ),

  adminApproveRequest: (id: string) =>
    authedFetch(`${apiBase()}/admin/requests/${encodeURIComponent(id)}/approve`, {
      method: 'POST',
    }).then(noContentOrThrow),

  adminDenyRequest: (id: string, reason: string) =>
    authedFetch(`${apiBase()}/admin/requests/${encodeURIComponent(id)}/deny`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reason }),
    }).then(noContentOrThrow),

  adminListSessions: () =>
    authedFetch(`${apiBase()}/admin/sessions`).then(jsonOrThrow<{ items: ABSSession[] }>),

  adminCloseSession: (id: string) =>
    authedFetch(`${apiBase()}/admin/sessions/${encodeURIComponent(id)}/close`, {
      method: 'POST',
    }).then(noContentOrThrow),

  adminListTokens: (userId?: string) => {
    const q = userId ? `?user_id=${encodeURIComponent(userId)}` : '';
    return authedFetch(`${apiBase()}/admin/tokens${q}`).then(jsonOrThrow<{ items: ABSToken[] }>);
  },

  adminRevokeToken: (id: string) =>
    authedFetch(`${apiBase()}/admin/tokens/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
    }).then(noContentOrThrow),
};

export const _internals = { apiBase };

function audiobookBackendCapability(capabilities: InstalledCapability[]): InstalledCapability | undefined {
  return capabilities.find((cap) => cap.type === 'audiobook_backend.v1');
}

function audiobookRoles(capability?: InstalledCapability): string[] {
  const roles = capability?.metadata?.audiobook_roles;
  return Array.isArray(roles)
    ? roles.filter((role): role is string => typeof role === 'string')
    : [];
}

function hasAudiobookRole(plugin: InstalledBackend, role: string): boolean {
  return plugin.audiobook_roles.includes(role);
}

async function fetchInstalledAudiobookPlugins(): Promise<InstalledBackend[]> {
  const res = await authedFetch('/api/v1/admin/plugins/installations');
  if (!res.ok) {
    const detail = await res.text().catch(() => '');
    throw new Error(
      `Could not load installed backends (HTTP ${res.status})${
        detail ? `: ${detail.slice(0, 200)}` : ''
      }`,
    );
  }
  const body = await res.json();
  const installations = Array.isArray(body) ? body : body.installations || [];
  return installations
    .filter((i: { enabled: boolean; capabilities?: InstalledCapability[] }) => {
      const capabilities = i.capabilities ?? [];
      return i.enabled && !!audiobookBackendCapability(capabilities);
    })
    .map(
      (i: {
        id: number;
        plugin_id: string;
        display_name?: string;
        enabled: boolean;
        capabilities?: InstalledCapability[];
        metadata?: Record<string, unknown>;
      }) => {
        const capabilities = i.capabilities ?? [];
        const audiobookBackend = audiobookBackendCapability(capabilities);
        return {
          id: i.id,
          plugin_id: i.plugin_id,
          enabled: i.enabled,
          capabilities,
          audiobook_backend: audiobookBackend,
          audiobook_roles: audiobookRoles(audiobookBackend),
          display_name:
            audiobookBackend?.display_name ||
            i.display_name ||
            (typeof i.metadata?.display_name === 'string' ? i.metadata.display_name : undefined) ||
            i.plugin_id,
          summary: audiobookBackend?.description,
        };
      },
    );
}

export async function fetchInstalledBackends(): Promise<InstalledBackend[]> {
  const plugins = await fetchInstalledAudiobookPlugins();
  return plugins.filter((plugin) => {
    if (hasAudiobookRole(plugin, 'library_source')) {
      return true;
    }
    if (plugin.audiobook_roles.length > 0) {
      return false;
    }
    return plugin.audiobook_backend?.metadata?.supports_catalog !== false;
  });
}

export async function fetchRequestProviders(): Promise<InstalledBackend[]> {
  const plugins = await fetchInstalledAudiobookPlugins();
  return plugins.filter((plugin) => {
    if (hasAudiobookRole(plugin, 'request_provider')) {
      return true;
    }
    return plugin.audiobook_backend?.metadata?.supports_catalog === false;
  });
}
