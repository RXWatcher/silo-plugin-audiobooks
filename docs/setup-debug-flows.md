# Audiobooks Portal: Setup, Routes, Flows, And Debugging

Plugin ID: `continuum.audiobooks`. Customer-facing portal + ABS-compatible
API + admin app. The README is the authoritative capability and config
reference; this document focuses on operating and debugging a deployed
plugin.

See also:

- `operations.md` — ongoing admin tasks (libraries, providers, secrets,
  podcasts, sessions).
- `troubleshooting.md` — symptom-to-cause table for the most common
  customer-reported failures.
- `AUDIOBOOK_PLAYER_QA.md` — manual player checklist after a release.
- `archive/` — historical specs (kept for context, not current).

## Setup checklist

1. Provision a dedicated Postgres role and `audiobooks` schema. The plugin
   creates all of its own tables inside that schema via embedded
   migrations at startup. The DSN passed in `database_url` MUST include
   `search_path=audiobooks` (e.g.
   `postgres://plugin_audiobooks:...@host:5432/continuum?search_path=audiobooks&sslmode=disable`).
2. Install the plugin in Continuum and set `database_url` in the host
   plugin config. This is the only host-level config the plugin reads —
   everything else lives in the plugin's own `backend_config` row.
3. Install at least one audiobook backend (`continuum-plugin-local-audiobooks`
   or `continuum-plugin-bookwarehouse-audio`). Optionally install
   `continuum-plugin-audiobook-requests`.
4. In the Audiobooks admin UI:
   - Pick the active backend (Settings).
   - Map presentation libraries to the backend's libraries/sub-libraries.
   - Set the shared **media signing secret** (must match the backend's
     `stream_signing_secret`).
   - Set the **ABS JWT secret** (auto-generated on first Configure; rotate
     here if needed).
   - If clients will connect via the official ABS mobile/desktop app, set
     `standalone_http_listen` (e.g. `127.0.0.1:9999`) and configure the
     reverse proxy to point a hostname directly at it. Restart the plugin
     after changing this value — see "Standalone listener" below.
   - Choose `standalone_login_mode` (`disabled` / `opt_in` / `all_accounts`).
5. Optional environment variables (read once at process start):
   - `CONTINUUM_HOST_URL` / `CONTINUUM_HOST_BASE_URL` — host base used to
     build self-referential stream URLs and the body-creds login target.
   - `CONTINUUM_PLUGIN_TOKEN` — service token for plugin-to-plugin reads
     (the scheduler uses this when reconciling requests).
   - `CONTINUUM_REDIS_URL` — enables the Socket.io Redis adapter when
     running multiple replicas. Unparseable or unreachable Redis logs a
     warning and falls back to the in-memory adapter (single-replica
     mode); it does not fail the boot.
   - `EMBEDDING_BASE_URL` / `EMBEDDING_MODEL` — enables the "similar
     books" recommender. Unset disables the recommender silently.
6. Verify with a portal browse, an admin "test backend" call, a request
   submit, and (if standalone is configured) an ABS-client login.

## Route inventory

All routes mount under the plugin's base path when invoked via the host
proxy (`/plugins/continuum.audiobooks/...`). The standalone listener
mounts the same set on `/` plus `/socket.io/*`.

| Method | Path | Access | Purpose |
| --- | --- | --- | --- |
| `*` | `/api/v1/*` | authenticated | Portal SPA REST API. |
| `*` | `/api/*` | authenticated | ABS-compatible mirror of `/abs/api/*` (some clients hit the bare prefix). |
| `GET` | `/abs/public/*` | public | Share-link audio + cover bytes. |
| `POST` | `/abs/api/login` | public | ABS login. Header path (host-proxied) and body-creds path (standalone). |
| `POST` | `/abs/api/auth/refresh` | public | Refresh-token rotation. |
| `GET` | `/abs/api/ping` | public | Health probe used by ABS clients. |
| `*` | `/abs/*` | authenticated | ABS-compatible API. |
| `GET` | `/assets/*` | public | SPA static assets. |
| `GET` | `/*` | authenticated | Customer SPA. |
| `GET` | `/admin`, `/admin/*` | admin | Admin SPA. |
| `*` | `/socket.io/*` | — | Realtime hub for ABS clients. **Standalone listener only.** Returns 503 when invoked via the host proxy because gRPC cannot bridge websocket upgrades. |

The standalone listener strips every inbound `X-Continuum-*` header
before invoking the handler (`internal/httproutes/server.go`). This is
the trust boundary: header-derived identity is only valid on the
host-proxied path; on the standalone listener identity is established by
credential validation against the host or by a previously-minted ABS JWT.

## Scheduled tasks

The host invokes these on the cadences declared in `manifest.json`. All
tasks no-op when `backend_config` is missing or no backend is selected.

| Task ID | Cron | Behaviour |
| --- | --- | --- |
| `request_reconciler` | host default tick | Polls the backend for `imported`/`failed` status of any request still in-flight, then runs the ABS session reaper (closes sessions idle >10 minutes). |
| `abs_session_reaper` | host default tick | Standalone session reaper (legacy entry point; same body as the reaper inside `request_reconciler`). |
| `portal_library_sync` | `0 * * * *` | Mirrors backend library metadata into `portal_libraries` for the SPA's shelf rendering. |
| `podcast_feed_refresher` | `*/10 * * * *` | Walks podcasts whose `refresh_at` has elapsed, upserts new episodes from RSS, emits `episode_download_finished` to connected ABS clients. Optional dep — no-ops if `podcastfeed.Refresher` isn't wired (it always is in production). |
| `purge_expired` | `0 */6 * * *` | Drops expired share-link rows and recommendation-cache rows. Idempotent. |

`cache_evictor` is recognised but is now a no-op: the previous portal
streaming cache was removed because the host plugin proxy capped response
bodies at 10 MiB, which silently broke any real audiobook file.

## Operational flows

### Browse / playback (SPA, host-proxied)

1. Customer opens the audiobooks SPA. The host's plugin proxy injects
   `X-Continuum-User-Id` after session validation.
2. SPA calls `/api/v1/...` for shelves; the portal reads
   `portal_libraries` and calls the configured backend for catalog
   details via the host runtime's `CallPluginHTTP` RPC.
3. Cover URLs and stream URLs are minted with a short-TTL signed media
   token (HS256, 15-minute TTL, audience `audiobook_backend`, claims
   `sub` / `book_id` / `file_idx`). The backend verifies with the same
   shared secret.
4. The web `<audio>` element follows a 302 from the portal stream route
   to the backend's stream route with `?token=...`. Browsers don't send
   `Authorization` headers on tag-issued requests, so the token must
   live in the URL.
5. Playback writes progress through `/abs/api/session/{sid}`. The portal
   publishes `user_item_progress_updated` via the Socket.io hub so other
   devices on the same account converge without polling.

### Browse / playback (ABS-mobile, standalone listener)

1. ABS client connects to the standalone listener (operator-configured
   hostname → `standalone_http_listen`).
2. Login: client POSTs `{username, password}` to `/abs/api/login`.
   - `standalone_login_mode = disabled` → 401 (header path only).
   - `standalone_login_mode = opt_in` → 401 unless the user has flipped
     "Allow mobile-app login" in the portal SPA (`abs_standalone_opt_ins`).
   - `standalone_login_mode = all_accounts` → any user with a local
     password may log in. OIDC-only users without a local password fail
     closed because the host's `LocalProvider.Authenticate` gates on
     `user.LocalPasswordLoginEnabled`.
   - Body-creds path POSTs `{username, password, provider: "local"}` to
     `{CONTINUUM_HOST_URL}/api/v1/auth/login` and reads `{user.id, user.name}`
     from a 200. Host 401/403 → 401; any other host failure → 502 (a
     misconfigured `CONTINUUM_HOST_URL` cannot silently succeed).
3. Successful login mints an access + refresh JWT pair (rows in
   `abs_tokens`) and returns the ABS envelope including
   `ServerVersion: 2.26`.
4. Client opens Socket.io to `/socket.io/...`, emits `auth` with the
   access JWT. Server validates signature against
   `backend_config.ABSJWTSecret`, checks the JTI is not revoked, joins
   the socket to `user:<id>`. Emits `init`; anything else fires
   `auth_failed` and disconnects.
5. Stream bytes flow through the plugin: `handlePublicTrack` mints a
   media token, calls the backend's stream route via `GetStream`, and
   copies bytes back with `Range` / `Content-Range` pass-through. This
   is bandwidth-heavy but unavoidable — standalone clients can't follow
   a redirect into a host-only stream URL.

### Request lifecycle

Statuses: `submitted` → `acknowledged` → `queued` → `downloading` →
`imported` (terminal) / `failed` (terminal) / `denied` / `cancelled`.

1. Customer submits via SPA → row inserted in `requests` with status
   `submitted`, plugin emits a `request_submitted` event targeted at the
   configured request provider.
2. Provider acknowledges → portal receives
   `plugin.continuum.audiobook-requests.request_acknowledged`,
   `status_watcher` updates the row's `external_id` + status.
3. Status changes flow through `request_status_changed`. Terminal
   `imported` events arrive either from the request provider
   (`request_fulfilled`) or directly from the backend
   (`audiobook_imported`). Either path resolves the row by
   `external_id`.
4. `audiobook_imported` additionally broadcasts `item_added` (and a
   singleton `items_added` array — real ABS emits both names) to every
   connected ABS client, so library shelves refresh without polling.
5. `request_reconciler` is the safety net: if any provider event was
   missed, every 5 minutes the scheduler polls the backend's
   `GetRequestSnapshot` and resolves anything in `imported` / `failed`
   to its terminal state.

### Realtime fan-out

- Source events: `PATCH /abs/api/me/progress/{itemId}`, `PATCH
  /abs/api/session/{sid}`, `POST /abs/api/items/{id}/play`, `POST
  /abs/api/session/{sid}/close`, plus consumer-side
  `audiobook_imported`.
- Distribution: in-process hub by default; with `CONTINUUM_REDIS_URL`
  set, the `zishang520/socket.io-go-redis` adapter fans events across
  replicas. The host runs a single replica today, so Redis is optional.
- A nil/unwired hub is tolerated everywhere — the publish path no-ops.
  Use `ConnectionCount()` (admin diagnostics) to confirm clients are
  actually attached.

## Debugging runbook

Most customer-facing problems map to one of these heads. Work them
top-down: a broken backend will look like a broken portal.

### 1. Customer SPA loads but shelves are empty

- Confirm `backend_config.target_backend_plugin_id` matches an installed,
  enabled backend installation.
- `portal_library_sync` runs hourly. After a fresh install, force a sync
  from the admin UI (or wait an hour) before declaring the SPA broken.
- Check `portal_libraries` rows for the active backend. Missing rows →
  the sync hasn't run or the backend returned no libraries.
- Verify the backend is reachable from the plugin process:
  `bkClient.ListCatalog` errors will appear in the plugin log at warn or
  error.

### 2. Playback fails ("This audio cannot be played")

- Check the portal stream response (Network tab in the SPA): a 302 with
  a `?token=` query string is correct.
- If the response is 503 with `media signing not configured`, the
  `media_signing_secret` is unset. Set it in the admin Settings — and
  set the **same** value as `stream_signing_secret` in the backend.
- The portal and the backend both run `decodeSecret` (try base64,
  fall back to raw bytes). A secret that happens to be valid base64
  (e.g. `dGVzdA==`) is decoded as base64 — this is consistent across
  both sides, but if the operator copy-pastes a high-entropy ASCII
  string that looks like base64 they will not get "what they typed"
  semantics. Pin a generated random secret rather than a chosen string.
- If the backend returns 401 for the stream URL, the secrets do not
  match. Token claims (`book_id`, `file_idx`) are visible in the token
  payload — decode it without verifying to confirm the portal minted
  what you expect, then cross-check the backend's verification log.

### 3. ABS mobile client cannot log in

- Confirm the reverse proxy points at the standalone listener address.
  Standalone-listener routes never go through the host plugin proxy.
- 401 with `not_enabled_for_mobile_login`: the user is in `opt_in` mode
  and has not flipped the toggle in the SPA. Either flip it for them
  via `POST /api/v1/me/abs-standalone` while authenticated as that
  user, or switch `standalone_login_mode` to `all_accounts`.
- 401 with the generic ABS error shape: host returned 401/403. Likely
  causes: wrong password, OIDC-only user without a local password, or
  the host's local auth provider is disabled. Check the host's
  `LocalProvider.Authenticate` log line.
- 502: `CONTINUUM_HOST_URL` is wrong or unreachable. Plugin host
  network must be able to resolve and reach the host's `/api/v1/auth/login`.
- 429-ish behaviour: the per-IP token bucket on body-creds login fired
  (30 req/s, burst 60, failed attempts only). Verify the client isn't
  retrying in a tight loop after a password change.
- Successful login but realtime events never arrive: see "Socket.io
  silently disconnects" below.

### 4. Socket.io silently disconnects, "Connecting..." in the client

- Reachable only on the standalone listener. The host-proxied
  `/socket.io/...` path returns 503 (`socket_not_ready`) by design.
- Auth failure → `auth_failed` event. The official ABS clients hide
  this; check the plugin log for `abssocket: auth rejected` with
  reason. Common reasons:
  - JWT signature mismatch — the ABS JWT secret was rotated after the
    client's token was minted.
  - Token revoked — operator revoked the row in `abs_tokens` (admin
    Tokens page). Client must re-login.
  - `server not ready` — plugin hasn't finished its Configure pass yet
    (storePtr nil). Wait or check startup logs for a migration failure.
- Connections that auth succeed but never receive events → multi-replica
  deployment without `CONTINUUM_REDIS_URL`. Publishing on replica A
  cannot reach a client connected to replica B. Set `CONTINUUM_REDIS_URL`
  and restart all replicas.

### 5. Postgres migration failure on startup

- Migrations run inside Configure (`internal/migrate.Run`). A failure
  here keeps the plugin in `not_ready` state (every HTTP request
  returns 503 `not_ready`).
- The runner forwards the underlying error verbatim. Common ones:
  - `relation "schema_migrations" already exists but ...` → another
    plugin or hand-applied DDL created `schema_migrations` in the
    schema. The audiobooks plugin owns the schema; remove the conflict.
  - `permission denied for schema` → the role in `database_url` lacks
    DDL rights on `audiobooks`. Grant `USAGE, CREATE` and re-run.
  - `dirty database version N. Fix and force version.` → a previous
    migration crashed half-applied. Manually correct the schema to the
    expected state for version N, then `UPDATE schema_migrations SET
    dirty = false`. Don't down-migrate production; the down files exist
    for local dev only.
- `database_url` MUST include `search_path=audiobooks` — without it the
  embedded DDL targets `public` and tables end up in the wrong schema,
  which presents as "table not found" errors at first query.

### 6. Stream token signature failures

- Symptom: portal returns 302 successfully but the backend returns 401
  on the redirected URL.
- Diagnose:
  1. Decode the JWT payload at the `?token=` parameter (no signature
     check). Confirm `aud=audiobook_backend`, `sub` is the user id, and
     `book_id` / `file_idx` match the request.
  2. Confirm `exp` is in the future. The TTL is 15 minutes; a client
     that paused for >15 minutes between minting and fetching will fail.
     The SPA refreshes the URL on `<audio>` error.
  3. Confirm the **byte-for-byte identical** secret is configured on
     both sides. `decodeSecret` tries base64 first, then raw bytes — a
     trailing newline copy-pasted into one side will produce a
     different key. Strip whitespace.
  4. If the portal is rotating the secret often, in-flight tokens
     minted under the old secret will fail validation. Don't rotate
     during a streaming session; do it during a maintenance window.

### 7. Plugin restart drops active sessions

- The standalone HTTP listener binds **once per process** via `sync.Once`.
  Changing `standalone_http_listen` in the admin UI logs a warn
  (`standalone_http_listen changed; restart the plugin to apply`) and
  keeps serving on the old address. A full restart is required.
- On SIGTERM/SIGINT the standalone listener gets a 10-second
  `Shutdown(ctx)` so in-flight ABS streams finish instead of being cut
  mid-byte. Long downloads past 10s **will** be killed; client
  resume-on-Range is the recovery path.
- Active ABS Socket.io connections are dropped on restart and clients
  reconnect, re-auth, and rejoin their user room automatically. The
  in-memory `connCount` resets to zero — don't read it as historical.
- Request reconciler is the recovery net for any
  `request_status_changed` event that arrives during the restart
  window. It runs on the host's scheduled-task tick after the plugin
  comes back up; the request row will catch up within one tick.

## Verification after changes

1. `make test` locally (Go + Vitest). Both pass before publishing.
2. Reload the installation in Continuum admin.
3. Hit `/abs/api/ping` (public) — should return 200 with the ABS ping
   envelope.
4. Open the SPA, exercise a browse + a 10-second playback, confirm the
   stream token landed and the backend served bytes.
5. If standalone is configured, log into the ABS mobile app and play one
   book through to a track transition.
6. For request flows, submit a tiny test request and watch
   `request_acknowledged` → `imported` in `requests`. Leave the
   reconciler one tick if the event arrives during a restart window.
