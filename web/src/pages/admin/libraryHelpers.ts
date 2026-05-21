import type { InstalledBackend, LibraryInfo } from '@/api/types';

function audiobookRoles(provider: InstalledBackend): string[] {
  return provider.audiobook_roles;
}

export function isRequestProvider(provider: InstalledBackend): boolean {
  return audiobookRoles(provider).includes('request_provider');
}

export function isLibrarySource(provider: InstalledBackend): boolean {
  if (audiobookRoles(provider).includes('library_source')) {
    return true;
  }
  if (audiobookRoles(provider).length > 0) {
    return false;
  }
  const supportsCatalog = provider.audiobook_backend?.metadata?.supports_catalog;
  if (supportsCatalog === false) {
    return false;
  }
  return true;
}

export function filterLibrarySourceBackends(
  providers: InstalledBackend[],
): InstalledBackend[] {
  return providers.filter(isLibrarySource);
}

export function resolveInstalledBackend(
  providers: InstalledBackend[],
  value?: string,
): InstalledBackend | undefined {
  if (!value) {
    return undefined;
  }
  return providers.find(
    (provider) => String(provider.id) === value || provider.plugin_id === value,
  );
}

export function normalizeLibrariesForSave(
  items: LibraryInfo[],
  providers: InstalledBackend[],
): LibraryInfo[] {
  return items.map((item, index) => {
    const provider = resolveInstalledBackend(providers, item.backend_plugin_id);
    return {
      ...item,
      media_type: item.media_type || 'audiobook',
      backend_plugin_id: provider ? String(provider.id) : item.backend_plugin_id,
      enabled: item.enabled ?? true,
      sort_order: index,
    };
  });
}
