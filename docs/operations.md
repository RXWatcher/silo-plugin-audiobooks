# Audiobooks Portal: Operations Guide

Ongoing tasks for the operator running this plugin. The setup steps and
debugging runbook live in `setup-debug-flows.md`. This document covers
the day-to-day surface: rotating secrets, configuring libraries, managing
sessions, podcast hygiene, share links, and what happens at restart.

## Admin-facing vs customer-facing surfaces

Two distinct user roles touch this plugin:

- **Admin / operator** — uses `/admin/*` (admin SPA). Owns
  `backend_config`, presentation library wiring, secrets, sessions,
  tokens, content restrictions, custom metadata providers, podcast
  force-refresh, request-provider selection, and the standalone listener
  configuration. Receives no automated notifications — this is a
  pull-based UI.
- **Customer / listener** — uses `/` (portal SPA), `/abs/*` (in-app
  client), or the official ABS mobile/desktop app via the standalone
  listener. Owns their own progress, bookmarks, reading-session
  history, smart collections, share links they created, notification
  preferences, and the per-user "Allow mobile-app login" toggle.

Most admin operations below have no customer-visible side effects until
the next page load or next backend event. Secret rotation and library
unwiring are the exceptions; see "Restart impact" below.

## Backend wiring

Single active backend at any time. `backend_config.target_backend_plugin_id`
identifies the installed backend plugin's instance.

- Swapping backends: change the selection in admin Settings. Existing
  `requests` rows keep their `external_id` pointing at the old backend
  — they become orphaned for reconciliation, but their final status
  (if already `imported` / `failed`) remains correct. Best practice:
  drain in-flight requests before swapping.
- Presentation libraries map a portal library to one of the backend's
  libraries (or a sub-library / bucket). Customers see only what's
  mapped. Unmapping a library hides it from the SPA immediately on the
  next render but does not delete progress or bookmarks.
- `portal_library_sync` runs hourly. Force a sync after wiring changes
  via the admin "Sync now" button rather than waiting.

## Secrets

Three secret fields, all in `backend_config`. None are stored in the
host plugin config.

| Field | Purpose | Rotation impact |
| --- | --- | --- |
| `media_signing_secret` | Shared HS256 secret for stream + cover tokens. Must match the backend's `stream_signing_secret`. | In-flight stream URLs minted under the old secret 401 at the backend. Rotate during a maintenance window or coordinate with the backend's secret simultaneously. |
| `abs_jwt_secret` | HS256 secret for ABS access + refresh tokens. Auto-generated on first Configure. | All existing ABS tokens become invalid; every connected ABS client and mobile client must re-login. Socket.io connections drop with `auth_failed`. |
| `standalone_login_mode` (not a secret, but lives here) | `disabled` / `opt_in` / `all_accounts` gate for the body-creds login path. | `disabled` immediately stops new logins; existing tokens remain valid until they expire or are revoked. |

The plugin's `EnsureBackendConfig` only generates a JWT secret when the
row is missing. Manually clearing the column (then restarting) will
mint a new one. Do this if you suspect compromise; expect every ABS
client to need re-login.

## Sessions and tokens

- **ABS sessions** (`abs_sessions`) — playback sessions opened by a
  client. Idle >10 minutes are reaped automatically by the scheduler.
  Admin Sessions page can revoke a single session immediately; the
  takeover flow uses the same path.
- **ABS tokens** (`abs_tokens`) — access + refresh JTIs. Revoking a
  token row sets `revoked_at`; the Socket.io auth check rejects the
  next "auth" emit from that JTI. The HTTP path checks the same row
  during request validation. Refresh-token rotation inserts new rows
  and revokes the old refresh JTI in the same transaction; a
  mid-rotation crash leaves the old token re-usable for one retry.
- **Continue Listening hide flag** — per-user, per-book bit on
  `user_book_progress`. Customers toggle this from the book detail
  page; admins should not touch it.

## Postgres schema

The plugin owns the entire `audiobooks` schema. Tables of operational
interest:

- `backend_config` — singleton config row. Admin UI writes; everything
  else reads.
- `requests` — request lifecycle. Status transitions are driven by
  status_watcher (events) and request_reconciler (polling).
- `abs_tokens`, `abs_sessions`, `abs_standalone_opt_ins` — auth +
  realtime client state.
- `portal_libraries`, `portal_library_books` — sync_state mirror of
  the backend's catalog metadata. Refreshed hourly.
- `podcasts`, `podcast_episodes` — RSS-mirrored state. `refresh_at`
  drives the 10-minute scheduler tick.
- `share_links`, `recommendation_cache` — both have `expires_at`
  cleaned every 6 hours by `purge_expired`.
- `smart_collections`, `reading_sessions`, `reading_goal`,
  `bookmarks`, `user_book_progress`, `notification_pref`,
  `content_restriction`, `custom_metadata_provider`, `embeddings` —
  feature-specific state. None require routine operator attention.

Migration runner is `internal/migrate`. Files are embedded into the
binary; no out-of-band schema management. Down-migration files exist
for local dev — do not run them in production.

## Podcasts

- The 10-minute `podcast_feed_refresher` walks `podcasts WHERE refresh_at <= now()`.
- Force-refresh from the admin Podcasts page — bypasses `refresh_at`,
  runs the same code path.
- Episodes are upserted by `(podcast_id, guid)`. A feed that recycles
  GUIDs will not duplicate; a feed that changes a GUID will create a
  duplicate (this is a podcast publisher bug, not ours).
- Each successful refresh that finds new episodes emits
  `episode_download_finished` to every connected ABS client (global
  broadcast, not user-scoped). Real ABS uses this name to refresh the
  "latest episodes" shelf.

## Recommender / embeddings

- Requires `EMBEDDING_BASE_URL` + `EMBEDDING_MODEL` env vars. The
  plugin reads them once at process start; changing them requires a
  restart.
- Backed by pgvector with an HNSW index on `embeddings`. The
  recommender no-ops cleanly when unconfigured — the "similar" shelf
  just disappears from the SPA.
- `recommendation_cache` is cleaned every 6 hours by `purge_expired`.
  Manual `DELETE FROM recommendation_cache` is safe; the next request
  recomputes.

## Share links

- Customer-created, public, audio-byte-serving. Bytes flow through
  `/abs/public/*` on whichever listener served the link (host-proxied
  or standalone — both work because the route is declared public).
- Tokens carry their own expiry. Expired rows are removed by
  `purge_expired` every 6 hours.
- Operators can revoke a customer's share link by deleting its row;
  the customer's SPA will reflect this on the next page load.

## Restart impact on running clients

The plugin process is the trust boundary; here is what survives and
what doesn't across a restart.

| State | Survives restart? | Notes |
| --- | --- | --- |
| Progress, bookmarks, requests, podcasts, libraries, smart collections | Yes | Persisted in Postgres. |
| ABS access + refresh tokens | Yes (signature + JTI) | Unless `abs_jwt_secret` was rotated; then every token is invalid. |
| ABS Socket.io connections | No | All clients reconnect and re-auth. `connCount` resets. |
| In-flight HTTP streams on the standalone listener | Up to 10s | Shutdown grace window; long streams beyond 10s are killed and the client should Range-resume. |
| Per-IP login rate-limit state | No | Token bucket is in-process. Operators can clear a temporary lockout by restarting. |
| `standalone_http_listen` binding | Process-scoped | Bound once via `sync.Once` per process; changing the value in admin requires a restart. |
| Embedding recommender config | Process-scoped | Read from env at startup. Changes require restart. |
| Single-replica Socket.io hub | Process-scoped | Multi-replica deployments need `SILO_REDIS_URL` to avoid losing cross-replica fan-out across the restart. |

## Routine maintenance checklist

Weekly:

- Skim the admin Sessions page for stale entries. The reaper handles
  the common case; manual revocation is only needed for compromised
  tokens.
- Check `purge_expired` log lines for the count of expired share links
  and recommendation cache rows. A zero count is normal; a count
  growing unboundedly suggests `purge_expired` is failing — check the
  scheduler logs.

Monthly:

- Confirm `portal_library_sync` is keeping pace. A backend that adds
  thousands of books in a single hour will fall behind; force a sync
  if the SPA's catalog count lags `bookwarehouse-audio`'s.
- Confirm the ABS JWT secret hasn't been rotated by accident. Check
  the access logs for an uptick in `auth_failed` events.

After any backend swap or secret rotation:

- Force-refresh podcast feeds (the refresher continues to work
  unchanged, but the safety net is cheap).
- Force a `portal_library_sync` from admin.
- Send one test stream from the SPA and one from the ABS mobile app
  to confirm both legs of the token contract still match.
