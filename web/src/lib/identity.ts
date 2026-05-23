// identity.ts derives the current user's identity from the host-stamped
// headers. Silo's plugin proxy passes user info through to the SPA via
// the `X-Silo-User-*` headers on every request the SPA makes back via
// authedFetch — but the SPA itself doesn't see headers on initial document
// load. Instead, we read from the optional `?role=` query parameter set by
// the sidebar link in silo, or default to "user".

let cachedRole: string | null = null;

export function captureRoleFromURL(params: URLSearchParams): void {
  const r = params.get('role');
  if (r) {
    cachedRole = r;
    sessionStorage.setItem('silo-role', r);
  } else {
    cachedRole = sessionStorage.getItem('silo-role');
  }
}

export function currentRole(): string {
  return cachedRole ?? sessionStorage.getItem('silo-role') ?? 'user';
}

export function isAdmin(): boolean {
  return currentRole() === 'admin';
}
