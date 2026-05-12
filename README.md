# continuum-plugin-audiobooks

Continuum plugin: customer-facing audiobook portal — browse, play, request,
collections, plus full Audiobookshelf mobile-app API compatibility.

See the design spec at
`/opt/worktrees/continuum-rh/docs/superpowers/specs/2026-05-11-audiobooks-portal-and-bookwarehouse-backend-design.md`.

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

## Build

```bash
make build         # builds web/dist + Go binary
make test          # go test + vitest
```

## Configuration

The plugin requires a single operator-supplied global config:

- `database_url` — Postgres DSN for the dedicated `audiobooks` schema.

Everything else (backend pick, streaming mode, ABS settings, request auto-approve)
lives in the `backend_config` singleton row and is admin-controlled via
`/admin/settings` in the SPA.

## Admin runbook

1. Provision the postgres role + schema:
   ```sql
   CREATE ROLE plugin_audiobooks LOGIN PASSWORD '<…>';
   CREATE SCHEMA audiobooks AUTHORIZATION plugin_audiobooks;
   ```
2. Install the plugin via the Continuum admin UI; configure `database_url`.
3. The schema migrates on first `Configure` (idempotent).
4. Open the portal SPA, navigate to `/admin/settings`, pick a backend plugin
   (e.g. `continuum.bookwarehouse-audio`), choose streaming mode, set
   approval gate, save.
5. Users access the portal via the user-side sidebar entry "Audiobooks".

## Known limitations

- Direct streaming mode is a stub — needs a filesystem-aware backend (future
  `continuum.audiobooks-fs` plugin) that exposes absolute paths.
- ABS `/login` handler trusts the inbound continuum identity; password
  validation against continuum's auth endpoint is deferred until the SDK
  exposes a host-side credential validator.
- Reconciler runs without a user bearer; backends that require one for
  `GET /api/v1/requests/{id}` will see empty status snapshots until the host
  exposes a service-token mechanism for cron-style cross-plugin calls.
