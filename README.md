# Audiobooks Portal for Continuum

`continuum.audiobooks` is Continuum's user-facing audiobook portal. It provides
the web app, request flow, playback surfaces, and Audiobookshelf-compatible
client API while delegating catalog, file, stream, and request fulfillment work
to audiobook backend plugins.

Install this plugin when you want a single audiobook experience in Continuum
that can sit in front of local libraries, external BookWarehouse instances, or
request providers such as `continuum.audiobookbay-requests`.

## Detailed Operations Docs

- [Setup, debugging, and communication flows](docs/setup-debug-flows.md)

## Features

- Authenticated Audiobooks web app for browsing, searching, requesting, and
  playing audiobooks.
- Public and authenticated Audiobookshelf-compatible routes for compatible
  clients.
- Admin-managed presentation libraries that can point at different backend
  plugins or backend sub-libraries.
- Request routing to a configured request provider plugin.
- Request status tracking for `submitted`, `acknowledged`, `queued`,
  `downloading`, `imported`, `failed`, `denied`, and `cancelled`.
- Optional standalone HTTP listener for reverse-proxied client routes.
- Optional CDN-style signed track URLs when paired with a compatible backend.
- Scheduled reconciliation for requests, idle sessions, and cached audio.

## Architecture

The portal is intentionally separate from source providers:

- `continuum.audiobooks` owns the user interface, ABS-compatible API,
  requests table, playback sessions, and library presentation.
- Catalog and stream providers such as `continuum.local-audiobooks` or
  `continuum.bookwarehouse-audio` own the actual library data.
- Request providers such as `continuum.audiobookbay-requests` can be selected
  separately from the catalog provider.

This keeps the customer-facing portal stable while operators can add, remove,
or swap providers underneath it.

## Configuration

| Key | Required | Description |
|---|---|---|
| `database_url` | yes | Postgres DSN using the `audiobooks` schema. |
| `standalone_http_listen` | no | Optional direct listener, for example `127.0.0.1:7878`, for reverse-proxied client routes. |
| `cdn_hostname` | no | Hostname used for presigned CDN-style track URLs. |
| `cdn_signing_secret` | no | 32-byte base64 HMAC secret shared with the streaming backend. |

Example DSN:

```text
postgres://plugin_audiobooks:password@postgres:5432/continuum?search_path=audiobooks&sslmode=disable
```

## Database Setup

```sql
CREATE ROLE plugin_audiobooks WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA audiobooks AUTHORIZATION plugin_audiobooks;
GRANT CONNECT ON DATABASE continuum TO plugin_audiobooks;
```

The plugin applies its migrations at startup.

## Provider Setup

After installing the portal:

1. Install at least one audiobook backend plugin, such as
   `continuum.local-audiobooks` or `continuum.bookwarehouse-audio`.
2. In the Audiobooks admin UI, create a presentation library and point it at
   the backend plugin or backend sub-library.
3. Optionally install a request provider, such as
   `continuum.audiobookbay-requests`, and select it in admin settings.
4. If using direct client streaming, configure `standalone_http_listen` and
   matching stream signing settings on the backend.

## HTTP Surface

| Route | Access | Purpose |
|---|---|---|
| `/api/v1/*` | authenticated | Portal REST API. |
| `/api/v1/libraries` | authenticated | Enabled presentation libraries. |
| `/api/v1/admin/*` | admin | Library and settings administration. |
| `/abs/public/*` | public | Public ABS-compatible assets. |
| `/abs/api/login` | public | ABS-compatible login endpoint. |
| `/abs/api/auth/refresh` | public | ABS-compatible token refresh. |
| `/abs/*` | authenticated | ABS-compatible API. |
| `/assets/*` | public | Web assets. |
| `/*` | authenticated | Audiobooks SPA. |

## Events

The portal listens for backend request and import events, including:

- `request_acknowledged`
- `request_status_changed`
- `request_fulfilled`
- `request_failed`
- `audiobook_imported`
- `audiobook_failed`

Acknowledgements may include a provider status such as `queued`; the portal
stores and displays that status so users can see whether a request is merely
accepted, queued, actively downloading, or fulfilled.

## Build And Test

```bash
go test ./...
cd web && npm run build
make build
```
