import { describe, expect, it } from 'vitest';

import type { InstalledBackend, LibraryInfo } from '@/api/types';
import {
  filterLibrarySourceBackends,
  isLibrarySource,
  isRequestProvider,
  normalizeLibrariesForSave,
  resolveInstalledBackend,
} from './libraryHelpers';

const localBackend: InstalledBackend = {
  id: 11,
  plugin_id: 'silo.local-audiobooks',
  display_name: 'Local Audiobooks',
  enabled: true,
  capabilities: [],
  audiobook_roles: ['library_source'],
  audiobook_backend: {
    type: 'audiobook_backend.v1',
    metadata: {
      audiobook_roles: ['library_source'],
    },
  },
};

const requestProvider: InstalledBackend = {
  id: 29,
  plugin_id: 'silo.audiobook-requests',
  display_name: 'Audiobook Requests',
  enabled: true,
  capabilities: [],
  audiobook_roles: ['request_provider'],
  audiobook_backend: {
    type: 'audiobook_backend.v1',
    metadata: {
      audiobook_roles: ['request_provider'],
      supports_catalog: false,
    },
  },
};

describe('libraryHelpers', () => {
  it('treats request-only backends as request providers, not library sources', () => {
    expect(isRequestProvider(requestProvider)).toBe(true);
    expect(isLibrarySource(requestProvider)).toBe(false);
    expect(isLibrarySource(localBackend)).toBe(true);
    expect(filterLibrarySourceBackends([localBackend, requestProvider])).toEqual([
      localBackend,
    ]);
  });

  it('resolves saved backend values by installation id or legacy plugin id', () => {
    expect(resolveInstalledBackend([localBackend], '11')).toEqual(localBackend);
    expect(resolveInstalledBackend([localBackend], 'silo.local-audiobooks')).toEqual(
      localBackend,
    );
  });

  it('normalizes library rows onto installation ids before save', () => {
    const libraries: LibraryInfo[] = [
      {
        id: 1,
        name: 'Main',
        media_type: 'audiobook',
        backend_plugin_id: 'silo.local-audiobooks',
        backend_library_id: 777,
        enabled: true,
        sort_order: 99,
      },
      {
        id: 2,
        name: 'Requests',
        media_type: 'podcast',
        backend_plugin_id: '29',
        enabled: false,
        sort_order: 98,
      },
    ];

    expect(normalizeLibrariesForSave(libraries, [localBackend, requestProvider])).toEqual([
      {
        ...libraries[0],
        backend_plugin_id: '11',
        sort_order: 0,
      },
      {
        ...libraries[1],
        backend_plugin_id: '29',
        sort_order: 1,
      },
    ]);
  });
});
