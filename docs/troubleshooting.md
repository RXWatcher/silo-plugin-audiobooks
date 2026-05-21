# Audiobooks Portal: Troubleshooting

Symptom-to-cause table. Pair this with `setup-debug-flows.md` (longer
walk-throughs) and `operations.md` (admin tasks).

## Customer-facing symptoms

| Symptom | First place to look | Common causes |
| --- | --- | --- |
| SPA loads but the home page has no shelves | `portal_libraries` rows for the active backend | No backend configured; `portal_library_sync` hasn't run yet (force from admin); backend returned no libraries; presentation libraries not mapped. |
| "Library not found" on a specific shelf | Admin → Libraries | The backend's library id changed (reinstall, rename) and the portal library is pointing at the old id. Re-map. |
| Cover image broken (404 or generic placeholder) | Network tab → cover URL has `?token=` | Missing media token (`media_signing_secret` unset) or token decoded against the wrong secret. Same root cause as broken playback. |
| Playback fails immediately ("This audio cannot be played") | Network tab → stream request status | Portal 503 (`media signing not configured`); backend 401 (secret mismatch); backend 410 (file moved); backend 404 (catalog stale, force sync). |
| Playback fails after a delay or scrubbing past a file boundary | Network tab → second-track URL | Token TTL (15 minutes) lapsed mid-listen — SPA should re-mint; if it doesn't, the SPA's audio-error handler is broken. File the bug. |
| Progress doesn't sync across devices | `user_book_progress` rows | Socket.io not connected on one device (check `auth_failed` in logs); multi-replica without `CONTINUUM_REDIS_URL`; one client wrote a stale "finished" sync that downgraded position-only on a finished book (real ABS protects this — confirm `is_finished` survives sync). |
| "Continue Listening" shows a book the customer hid | `user_book_progress.hide_from_continue` | Sync from an ABS-compatible client that doesn't know about the hide flag re-wrote progress. Customer can re-hide; not a portal bug. |
| Smart collection returns 0 books | Smart collection DSL preview | DSL evaluator failed silently — check plugin log for `smartcoll` errors. Often a typo in the DSL or referencing a tag that no longer exists. |
| Reading streak resets unexpectedly | `reading_sessions` rows for the user | Reading sessions are bucketed by local date; a customer crossing time zones can lose a day. Known, not fixable without per-user TZ. |
| Share link returns 404 | `share_links` row | Expired (cleaned every 6 hours by `purge_expired`); explicitly revoked; share-link host config changed. |
| Atmosphere/e-ink modes don't persist | Customer's settings cookie | Customer cleared browser storage. Not a server-side state. |

## ABS-client symptoms (mobile, desktop, third-party)

| Symptom | First place to look | Common causes |
| --- | --- | --- |
| "Server unreachable" on first launch | Reverse proxy → standalone listener | Proxy not configured; `standalone_http_listen` unset; plugin process not bound (check startup log for "standalone http listener starting"); listener bound to localhost while proxy is remote. |
| Login fails with the ABS error envelope | Plugin log → `abs.standalone_login` warn | `standalone_login_mode=disabled` (toggle in admin); user not in `abs_standalone_opt_ins` and mode is `opt_in`; host returned 401 (wrong password, OIDC-only user); host returned 5xx → portal returns 502. |
| Login fails repeatedly, then "Too many attempts" | Plugin log → `loginLimiter` | Per-IP 30 req/s burst 60 exhausted by failed body-creds attempts. Wait or restart the plugin to clear (in-process bucket). |
| Login succeeds, library empty | ABS client's view of `/abs/api/libraries` | Token mints fine but the user has zero presentation libraries mapped to their account. Map at least one library in admin. |
| Login succeeds, library populated, playback fails | Network capture of the stream URL | Standalone-listener clients stream **through** the plugin (bytes copied, not redirected). If the backend's `GetStream` fails, the plugin returns 5xx. Check backend health. |
| Login succeeds but no realtime updates | Plugin log → `abssocket` lines | Socket.io connection never authenticated (missing/malformed token, JWT secret mismatch, JTI revoked); multi-replica without Redis adapter; firewall blocking the websocket upgrade between proxy and plugin. |
| Sudden mass disconnect of every ABS client | Plugin log → recent admin actions | `abs_jwt_secret` was rotated. Every previously-minted token is now invalid; clients re-login. |
| Refresh fails with 401 | `abs_tokens` row for the refresh JTI | Refresh token was already rotated (concurrent double-refresh — tolerated, one wins); revoked by admin; expired (refresh TTL is longer than access but not infinite). |

## Operator / admin symptoms

| Symptom | First place to look | Common causes |
| --- | --- | --- |
| Plugin stays in "not_ready" (503 on every route) | Plugin process log | Migration failure; `database_url` missing or malformed; `database_url` lacks `search_path=audiobooks` (tables exist but in the wrong schema); Postgres connection refused. |
| `migrate: dirty database version N` on startup | `schema_migrations` table | A previous migration crashed half-applied. Manually correct the DDL to the expected end state, then `UPDATE schema_migrations SET dirty = false`. Down-files are dev-only. |
| Admin UI shows "backend not configured" after restart | `backend_config.target_backend_plugin_id` | Singleton row was wiped or the backend installation id changed (reinstall). Re-select the backend in Settings. |
| `standalone_http_listen` change "won't take effect" | Plugin log | The listener is bound once via `sync.Once`. Log warns `standalone_http_listen changed; restart the plugin to apply`. Restart the plugin. |
| Request rows stuck in `acknowledged` or `downloading` | Provider plugin logs + `request_reconciler` log | Provider isn't emitting `request_status_changed`; provider plugin is restarting; reconciler hasn't ticked yet (5min default); `external_id` mapping was never written because the consumer received the event before Configure finished (nack-and-retry path). |
| Podcast episodes not refreshing | `podcasts.refresh_at` values + scheduler log | All podcasts have `refresh_at > now()`; force-refresh from admin; the feed RSS URL is returning 4xx (plugin logs feed-level errors). |
| "Similar books" shelf disappeared | `EMBEDDING_*` env + recommender warn lines | Env vars were unset on restart; embedding API unreachable; pgvector index missing (verify the `0016_embeddings` migration ran). |
| Disk filling with cached files | `internal/cdn/`, scheduled cache evictor | The portal's old streaming cache was **removed**. If files are accumulating, look elsewhere — the backend, the host, podcast downloads from the backend, etc. |
| Plugin restarts cut customer streams | Standalone listener shutdown grace | 10-second `Shutdown(ctx)` is the cap. Long downloads beyond 10s are killed; clients should Range-resume. Schedule restarts for low-traffic windows. |
| Two plugin replicas, events only land on one | `CONTINUUM_REDIS_URL` | Single-replica in-memory hub. Set the Redis URL on all replicas and restart. Unparseable URL falls back to in-memory with a warn — check log for the warn. |

## Quick diagnostic recipes

### Decode a stream token

The token at `?token=...` on a stream URL is a JWT. Paste the payload
into jwt.io (no signature check needed) and verify:

- `aud` is `audiobook_backend`.
- `sub` matches the user.
- `book_id` and `file_idx` match the requested track. `file_idx` is
  `-1` for a cover token (`mediatoken.CoverFileIdx`).
- `exp` is in the future. The TTL is 15 minutes (`mediatoken.DefaultTTL`).

If the token looks right but the backend still 401s, the **secrets do
not match**. Cross-check `backend_config.media_signing_secret` against
the backend's `stream_signing_secret`. Both sides run a "decode as
base64 first, raw bytes second" helper — a leading/trailing whitespace
character will flip which branch wins.

### Confirm the standalone listener is bound

```
$ grep "standalone http listener starting" plugin.log
```

If absent, the plugin never reached the bind block — either
`standalone_http_listen` is unset in `backend_config`, or Configure
hasn't run yet (still in `not_ready`). If present but clients can't
connect, the address is probably `127.0.0.1:...` while the reverse
proxy lives on a different host.

### Confirm Socket.io fan-out works

Connect from any account with a valid ABS access token. Server should
log `abssocket: connection opened` then `abssocket: auth ok` and emit
`init` to the client. Then trigger a progress update on a different
device. The first client should receive `user_item_progress_updated`.

If progress doesn't arrive: check `ConnectionCount()` (admin
diagnostics endpoint) — if it's >0 but the test client is one of them
and isn't receiving, the user-room join failed (likely a JWT `sub`
mismatch). If `ConnectionCount()` is 0 despite an open connection in
the browser's DevTools, the connection authed against a different
plugin replica (multi-replica without Redis adapter).

### Replay a stuck request

Find the request in the admin Requests page, copy its `external_id`,
then either:

- Wait for `request_reconciler` to tick (≤5 minutes) — it will poll
  the backend's `GetRequestSnapshot` and resolve `imported` / `failed`
  to terminal state.
- Or, if the provider is healthy, ask the provider plugin to re-emit
  the event. The consumer is idempotent: receiving `request_fulfilled`
  for an already-fulfilled row is a no-op.
