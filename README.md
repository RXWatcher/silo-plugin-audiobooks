# Audiobooks Portal Plugin

`continuum.audiobooks` is the customer-facing audiobook experience for
Continuum. It provides the web portal, playback surfaces, request flow, and
Audiobookshelf-compatible client API while delegating catalog and streaming
data to backend plugins.

## What It Does

- Serves the authenticated Audiobooks web app.
- Exposes REST APIs for browsing, playback sessions, requests, and client
  integrations.
- Provides public and authenticated Audiobookshelf-compatible routes.
- Watches backend request/import events.
- Reconciles request state, closes idle sessions, and evicts cached audio.
- Supports optional standalone HTTP serving for reverse-proxied client-app
  access.

## Capabilities

| Capability | ID | Purpose |
|---|---|---|
| `http_routes.v1` | `portal` | User-facing SPA, REST API, and ABS-compatible routes. |
| `event_consumer.v1` | `status_watcher` | Tracks backend fulfillment and import events. |
| `scheduled_task.v1` | `request_reconciler` | Reconciles requests, sessions, and cache state. |

## HTTP Routes

| Route | Access | Purpose |
|---|---|---|
| `/api/v1/*` | authenticated | Portal API. |
| `/abs/public/*` | public | Public ABS-compatible assets. |
| `/abs/api/login` | public | ABS-compatible login. |
| `/abs/api/auth/refresh` | public | ABS-compatible token refresh. |
| `/abs/api/ping` | public | Client health/ping endpoint. |
| `/abs/*` | authenticated | ABS-compatible authenticated API. |
| `/assets/*` | public | Static web assets. |
| `/*` | authenticated | Navigable user SPA labelled `Audiobooks`. |

## Configuration

| Key | Required | Description |
|---|---|---|
| `database_url` | yes | Postgres DSN using the `audiobooks` schema. |
| `standalone_http_listen` | no | Optional direct listener for client-app routes. |
| `cdn_hostname` | no | Optional hostname for presigned CDN-style track URLs. |
| `cdn_signing_secret` | no | Base64 HMAC secret shared with the audiobook backend. |

Example `database_url`:

```text
postgres://plugin_audiobooks:password@postgres:5432/continuum?search_path=audiobooks&sslmode=disable
```

## Database Setup

```sql
CREATE ROLE plugin_audiobooks WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA audiobooks AUTHORIZATION plugin_audiobooks;
GRANT CONNECT ON DATABASE continuum TO plugin_audiobooks;
```

## Backend Integration

The portal expects one or more audiobook backend providers to expose catalog,
cover, and streaming behavior. For local M4B libraries, use
`continuum.audiobooksdb`.

The portal listens for backend state changes and also runs periodic
reconciliation so missed events do not permanently strand requests.

## Standalone And CDN Modes

`standalone_http_listen` lets the plugin bind a direct TCP listener such as
`:7878`. This is useful for mobile audiobook clients and reverse-proxy setups.
Protected SPA routes still require a Continuum session, while public client-app
routes can be reached directly.

`cdn_hostname` and `cdn_signing_secret` allow the portal to emit presigned track
URLs that point at a backend or reverse-proxied streaming host. The signing
secret must match the backend stream verification secret.

## Build And Test

```bash
go test ./...
go build -buildvcs=false -o continuum-plugin-audiobooks ./cmd/continuum-plugin-audiobooks
```

If frontend assets change, build the web project before packaging.

## Operational Notes

- Keep the portal and backend cache/signing settings aligned.
- Use HTTPS in front of standalone client-facing routes.
- The scheduled reconciler is designed to be idempotent.
- Monitor request failure counts after changing backend provider configuration.

## Repository Status

This is a first-party Continuum plugin owned by the Continuum project.
