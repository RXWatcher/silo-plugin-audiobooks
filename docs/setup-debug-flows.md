# Audiobooks Portal Setup, Debugging, And Flows

Plugin ID: `continuum.audiobooks`
Version documented: `0.1.0`

## Purpose

user-facing audiobook web app, request UI, playback surface, and Audiobookshelf-
compatible API.

## Runtime Dependencies

- Continuum plugin host
- Postgres schema for this plugin
- At least one audiobook backend such as local-audiobooks or bookwarehouse-audio
- Optional request provider such as audiobookbay-requests

## Setup Checklist

1. Create the audiobooks schema and configure database_url.
2. Install one or more audiobook backend plugins.
3. In the Audiobooks admin UI, create presentation libraries mapped to backend installations/libraries.
4. Optionally configure standalone_http_listen for reverse-proxied ABS/mobile client access.
5. Optionally configure cdn_hostname and cdn_signing_secret if a backend serves signed track URLs.
6. Submit a test browse, playback, and request flow.

## Configuration Reference

- `database_url`
- `standalone_http_listen`
- `cdn_hostname`
- `cdn_signing_secret`

Use the plugin manifest/admin form as the source of truth for field validation and defaults. Keep database credentials scoped to the plugin schema unless a plugin explicitly needs read access to Continuum core tables.

## Exposed Routes

- `* /api/v1/* [authenticated]`
- `GET /abs/public/* [public]`
- `POST /abs/api/login [public]`
- `POST /abs/api/auth/refresh [public]`
- `GET /abs/api/ping [public]`
- `* /abs/* [authenticated]`
- `GET /assets/* [public]`
- `GET /* [authenticated]`

## Capabilities

- `http_routes.v1 (portal) - Customer-facing audiobook browser, player, request flow, and ABS-mobile API.`
- `event_consumer.v1 (status_watcher) - Tracks request fulfillment and import status from configured audiobook backends.`
- `scheduled_task.v1 (request_reconciler) - Polls backend for missed status events; closes idle ABS sessions; LRU-evicts cached audio.`

## Operational Flows

### Browse/playback

1. User opens the Audiobooks SPA or ABS-compatible client.
2. The portal loads presentation libraries and calls the selected backend plugin for catalog/detail/search data.
3. For playback, the portal creates session state and requests stream URLs/ranges from the backend.
4. Progress and request status remain in the portal database.

### Request

1. User submits a request in the portal.
2. The portal stores the request and emits a provider-targeted request_submitted event.
3. A request provider acknowledges/updates the request.
4. The portal displays queued/downloading/imported/failed state to users.

## How This Plugin Communicates

- Calls audiobook_backend.v1 providers for catalog, detail, search, cover, and stream operations.
- Emits request events to request providers.
- Consumes request/import status events from providers.

## Debugging Runbook

- If the Available app loads but libraries are empty, verify presentation library mappings.
- If playback fails, test backend stream endpoints and signed URL settings before debugging the SPA.
- For ABS clients, validate reverse proxy paths for /abs/* and token refresh.
- Check status_watcher and request_reconciler scheduled task logs for stuck requests.
- Confirm backend plugin IDs and installation IDs after reinstalling providers.

## Log And Health Checks

- Start with Continuum Admin -> Plugins and confirm the installation is enabled.
- Check the plugin process logs around startup for manifest loading, migration, and route registration.
- Check scheduled task logs when a workflow depends on polling or reconciliation.
- Confirm the plugin routes are reachable through Continuum using the access level shown above.
- For database-backed plugins, verify the configured role can connect, create/migrate tables in its schema, and read/write expected rows.

## Common Failure Patterns

- Wrong installation ID selected in a portal or router setting after reinstalling a plugin.
- Plugin database URL points at the public schema instead of the dedicated plugin schema.
- Reverse proxy forwards the SPA route but not `/api/*`, `/api/v1/*`, `/assets/*`, or provider-specific public routes.
- Network checks are run from the operator laptop instead of from the Continuum/plugin runtime network.
- Secrets are regenerated during restart, invalidating signed URLs, encrypted fields, or login state.

## Verification After Changes

1. Restart or reload the plugin installation.
2. Open the plugin route or admin page in Continuum.
3. Exercise the smallest workflow that crosses a plugin boundary.
4. Confirm both the source plugin and destination plugin record the same request/session/login identifier.
5. Leave the scheduled reconciler enough time to run, then confirm terminal state or a useful error.
