import { mountPath } from '@/lib/mountPath';
import { getCachedToken } from '@/lib/auth';
import type {
  ABSSession,
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
  NarratorSummary,
  PageEnvelope,
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

export async function authedFetch(input: string, init?: RequestInit): Promise<Response> {
  const headers = {
    ...(init?.headers as Record<string, string> | undefined),
    ...authHeaders(),
  };
  return fetch(input, { ...init, headers });
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

  adminGenerateStreamingSecret: () =>
    authedFetch(`${apiBase()}/admin/generate-streaming-secret`, {
      method: 'POST',
    }).then(jsonOrThrow<{ secret: string }>),
};

export const _internals = { apiBase };

function audiobookBackendCapability(capabilities: InstalledCapability[]): InstalledCapability | undefined {
  return capabilities.find((cap) => cap.type === 'audiobook_backend.v1');
}

export async function fetchInstalledBackends(): Promise<InstalledBackend[]> {
  const res = await fetch('/api/v1/admin/plugins/installations', {
    headers: authHeaders(),
    credentials: 'include',
  });
  if (!res.ok) return [];
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
