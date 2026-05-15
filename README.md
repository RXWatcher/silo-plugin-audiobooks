# continuum-plugin-audiobooks

Customer-facing audiobook portal for Continuum — catalog browsing, in-browser player, requests, collections, and a full Audiobookshelf-compatible API so the standard Audiobookshelf mobile apps connect natively.

This plugin is the **portal**, not a source of audiobooks. Pair it with an `audiobook_backend.v1` provider such as [`continuum.audiobooksdb`](../continuum-plugin-audiobooksdb/) (local files) or [`continuum.bookwarehouse-audio`](../continuum-plugin-bookwarehouse-audio/) (upstream BookWarehouse).

## Capabilities

| Capability | Notes |
|---|---|
| `http_routes.v1` (`portal`) | SPA, `/api/v1/*`, ABS-compat API (`/abs/api/*`, `/abs/public/*`). Navigation label "Audiobooks". |
| `event_consumer.v1` (`status_watcher`) | Listens for `request_acknowledged`, `request_failed`, `audiobook_imported`, `audiobook_failed` from configured backends. |
| `scheduled_task.v1` (`request_reconciler`) | Reconciles missed backend status; reaps idle ABS sessions; LRU-evicts cached audio. |

## Configuration

| Key | Required | Description |
|---|---|---|
| `database_url` | yes | DSN for the dedicated `audiobooks` Postgres schema. |
| `standalone_http_listen` | no | Bind a second TCP listener so external clients (mobile apps) can connect without an installation-id in the URL. See below. |
| `cdn_hostname` | no | Hostname (e.g. `audiobooks-cdn.example.com`) that reverse-proxies to the audiobooksdb plugin's standalone listener. Enables presigned-URL track streaming. |
| `cdn_signing_secret` | no | 32-byte base64 HMAC secret. **Must match** audiobooksdb's `stream_signing_secret`. Required if `cdn_hostname` is set. |

Backend selection, streaming mode, ABS settings, and request auto-approval are managed in the in-app `/admin/settings` page, not in global config.

## Standalone HTTP port

By default the plugin's HTTP routes are served via the Continuum host at `/api/v1/plugins/<installation_id>/*`. Setting `standalone_http_listen` makes the plugin **also** bind a TCP listener that serves the same handler tree directly, so operators can reverse-proxy a clean hostname (e.g. `abs.example.com`) at it. The Audiobookshelf mobile app and similar third-party clients can then point at `https://abs.example.com/` without an install-id in the URL.

**Config value**: a Go `net.Listen` address such as `:7878` (bind all interfaces) or `127.0.0.1:7878` (proxy-only — recommended unless the reverse proxy lives elsewhere).

**What answers on the standalone port**:

| Route | Behavior on the standalone port |
|---|---|
| `/abs/api/ping`, `/abs/api/login`, `/abs/api/auth/refresh`, `/abs/public/*` | 200 / normal — client apps work |
| `/abs/*` (authenticated ABS) | Handler-defined; ABS clients carry their own bearer so this generally works |
| `/api/v1/*`, `/*` (SPA) | **401** — these require host-injected `X-Continuum-User-*` headers which the standalone listener never sets. This is intentional. |

Changing the value requires a plugin restart; the listener is bound once at first `Configure`. The bind address is logged at start (`standalone http listener starting`) and any error is logged loudly.

**Example nginx**:

```nginx
server {
    listen 443 ssl http2;
    server_name abs.example.com;

    ssl_certificate     /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:7878;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

**Security**: binding to `:7878` exposes the listener on every interface. Prefer `127.0.0.1:7878` if a reverse proxy is on the same host. The standalone listener does **not** terminate TLS — that's the reverse proxy's job.

## Layout

```
cmd/continuum-plugin-audiobooks/  binary entrypoint + manifest.json
internal/
  abs/         Audiobookshelf-compat API (mounted at /abs/api/*)
  auth/        host-stamped identity middleware (X-Continuum-User-*)
  backend/     typed HTTP client for the configured audiobook_backend.v1
  consumer/    event_consumer.v1 — subscribes to backend status events
  event/       outbound event publisher wrapper
  httproutes/  HttpRoutes.v1 capability adapter
  migrate/     0001-0004 schema migrations (embedded SQL)
  runtime/     Configure handler
  scheduler/   scheduled_task.v1 — reconciler / session reaper / cache evictor
  server/      chi mux composing API + ABS + SPA
  store/       per-table DB wrappers
  streaming/   proxy / cache / direct streaming modes (router + cache)
  testutil/    Postgres testcontainers helper
web/           Vite + React 19 SPA (Tailwind v4 + shadcn new-york zinc)
```

## Dependencies

- Postgres role + `audiobooks` schema.
- At least one `audiobook_backend.v1` provider plugin.

## Install

```sql
CREATE ROLE plugin_audiobooks LOGIN PASSWORD '<chosen>';
CREATE SCHEMA audiobooks AUTHORIZATION plugin_audiobooks;
```

1. Install the plugin via the Continuum admin UI; configure `database_url`.
2. The schema migrates on first `Configure` (idempotent).
3. Open the portal SPA, navigate to `/admin/settings`, pick a backend plugin, choose streaming mode, set approval gate, save.
4. Users access the portal via the user-side sidebar entry "Audiobooks".

## Build & test

```bash
make build         # builds web/dist + Go binary
make test          # go test + vitest
```

## Known limitations

- Direct streaming mode is a stub — needs a filesystem-aware backend (future `continuum.audiobooks-fs`) that exposes absolute paths.
- ABS `/login` trusts the inbound continuum identity; password validation against continuum's auth endpoint is deferred until the SDK exposes a host-side credential validator.
- The reconciler runs without a user bearer; backends that require one for `GET /api/v1/requests/{id}` will see empty status snapshots until the host exposes a service-token mechanism for cron-style cross-plugin calls.

## Status

v0.1.0, beta. ABS API surface is functional with the Audiobookshelf mobile app.
