// Active profile id, captured from the ?profileId= query param the core
// app puts on the plugin SPA URL, cached in sessionStorage. Empty string
// means the primary profile.
const KEY = 'silo.profileId';

export function captureProfileFromURL(): void {
  const v = new URLSearchParams(window.location.search).get('profileId');
  if (v !== null) sessionStorage.setItem(KEY, v);
}

export function currentProfileId(): string {
  return sessionStorage.getItem(KEY) ?? '';
}
