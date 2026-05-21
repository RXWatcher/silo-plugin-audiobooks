# Audiobooks Portal for Continuum

`continuum.audiobooks` is the customer-facing audiobook portal in the Continuum plugin ecosystem. It owns the web SPA, the Audiobookshelf-compatible client API, the request lifecycle, library presentation, and playback sessions, while delegating catalog, file, and stream work to pluggable audiobook backends (local filesystem, BookWarehouse, etc.) and request fulfillment to a separate request-provider plugin.

## Category

Lives under **Books/Audiobooks** in the admin sidebar.

## Capabilities

| Type | ID | Purpose |
| --- | --- | --- |
| `http_routes.v1` | `portal` | Audiobooks SPA, portal REST API at `/api/v1/*`, ABS-compatible API under `/abs/*` and `/api/*`, and the admin app at `/admin/*`. Also serves a Socket.io endpoint on an optional standalone HTTP listener for native ABS clients (the host plugin proxy can't bridge websocket upgrades). |
| `event_consumer.v1` | `status_watcher` | Receives request lifecycle and import events from the configured audiobook backend and from the audiobook-requests provider, updates the matching request row, and broadcasts shelf-refresh hints to connected ABS clients. |
| `scheduled_task.v1` | `request_reconciler` | Polls the backend for missed status events and closes idle ABS playback sessions. |
| `scheduled_task.v1` | `portal_library_sync` | Hourly mirror of backend libraries (audiobooks, podcasts, share buckets) into the portal's presentation DB. |
| `scheduled_task.v1` | `podcast_feed_refresher` | Every 10 minutes, walks podcasts whose refresh window has elapsed and upserts new episodes from their RSS feeds. Emits `episode_download_finished` to connected clients. |
| `scheduled_task.v1` | `purge_expired` | Every 6 hours, drops expired share links and recommendation-cache rows. Idempotent. |

## Dependencies

This is a portal plugin — it does not own any audiobook files itself. Operators pair it with at least one backend, and optionally a request provider:

- Catalog/stream backends (audiobook_backend): [`continuum-plugin-local-audiobooks`](https://github.com/RXWatcher/continuum-plugin-local-audiobooks) for filesystem libraries, [`continuum-plugin-bookwarehouse-audio`](https://github.com/RXWatcher/continuum-plugin-bookwarehouse-audio) for an external BookWarehouse instance.
- Request provider: [`continuum-plugin-audiobook-requests`](https://github.com/RXWatcher/continuum-plugin-audiobook-requests) handles the actual sourcing of new titles.

The host app is [`ContinuumApp/continuum`](https://github.com/ContinuumApp/continuum) and the plugin contract lives in [`ContinuumApp/continuum-plugin-sdk`](https://github.com/ContinuumApp/continuum-plugin-sdk).

## External services

- **Postgres** — the plugin owns its own `audiobooks` schema for presentation libraries, requests, ABS sessions, podcasts, smart collections, share links, recommendation cache, and bookmarks. Migrations run on startup.
- **Redis** (optional) — when `CONTINUUM_REDIS_URL` is set, the Socket.io hub uses a Redis adapter so events on one plugin replica reach clients connected to another. Unset means single-replica in-memory.
- **Embedding API** (optional) — when `EMBEDDING_BASE_URL` / `EMBEDDING_MODEL` are set, the recommender powers a "similar books" shelf via an OpenAI/Gemini/Ollama-compatible endpoint. Unset means the recommender no-ops.
- **OpenLibrary / Google Books** — free metadata enrichment used by the admin "enrich" action on sparse imports.
- **Backend plugins** — called over the host's plugin proxy for catalog reads and the stream redirect target.
- **Continuum host auth** — the standalone ABS login path posts body credentials to the host's `POST /api/v1/auth/login` so this plugin never touches a password itself.

## Configuration

| Key | Required | Purpose |
| --- | --- | --- |
| `database_url` | yes | DSN for the dedicated `audiobooks` Postgres schema, e.g. `postgres://plugin_audiobooks:...@host:5432/continuum?search_path=audiobooks&sslmode=disable`. |

All other portal settings — target backend, presentation libraries, request provider, media signing secret, ABS JWT secret, optional standalone HTTP listen address, content restrictions, custom metadata providers — are managed in the Audiobooks admin UI and persisted in this plugin's database.

Relevant environment variables read at startup: `CONTINUUM_HOST_URL` / `CONTINUUM_HOST_BASE_URL` (host base for self-referential stream URLs), `CONTINUUM_PLUGIN_TOKEN` (host service token), `CONTINUUM_REDIS_URL` (optional multi-replica Socket.io adapter), and the `EMBEDDING_*` family used by the recommender.

## Event subscriptions

```text
plugin.continuum.bookwarehouse-audio.*
plugin.continuum.audiobook-requests.*
```

Specifically: `request_acknowledged`, `request_status_changed`, `request_fulfilled`, `request_failed`, plus `audiobook_imported` / `audiobook_failed` from the BookWarehouse backend.

## Customer-facing features

- Audiobooks SPA with home, library, authors, narrators, series, collections, smart collections, podcasts, stats, my-requests, settings, and a detail/player surface.
- Web player plus a Socket.io realtime channel and Audiobookshelf-compatible REST API for the official ABS mobile/desktop clients, terminated on an optional standalone HTTP listener.
- Request flow with statuses `submitted`, `acknowledged`, `queued`, `downloading`, `imported`, `failed`, `denied`, `cancelled`.
- Bookmarks, listening progress, and ABS-session reaping.
- Signed short-TTL media tokens on cover and stream URLs — a leaked URL grants access to one file for a small window, not the user's account.

## Admin / operator features

- Presentation libraries pointing at backend plugins or backend sub-libraries.
- Request provider selection and per-request inspection.
- Sessions overview and revocable tokens.
- Content restrictions and custom metadata providers.
- Force-refresh for podcast feeds; admin "enrich" against OpenLibrary / Google Books.

## Detailed docs

- [Setup, routes, flows, and debugging](docs/setup-debug-flows.md)
- [Operations guide](docs/operations.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Audiobook player QA](docs/AUDIOBOOK_PLAYER_QA.md)
- [Archive (historical specs)](docs/archive/)

## Build and release

Local build with `make build` (builds the Vite SPA under `web/` then the Go binary). Tests with `make test`.

CI builds linux-amd64 binaries on push to main via the reusable workflow in [RXWatcher/continuum-plugin-repository](https://github.com/RXWatcher/continuum-plugin-repository) and publishes them to the catalog at [`./binaries/`](https://github.com/RXWatcher/continuum-plugin-repository/tree/main/binaries).
