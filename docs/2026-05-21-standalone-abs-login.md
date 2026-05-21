# Standalone-port ABS login

Date: 2026-05-21
Owner: audiobooks plugin
Status: in-progress

## Problem

The audiobooks plugin exposes an Audiobookshelf-compatible API at `/abs/api/*`.
Listeners reverse-proxy a hostname (e.g. `abs.example.com`) directly to the
plugin's standalone HTTP listener so the official Audiobookshelf mobile and web
clients can talk to it without going through the Continuum host.

Today the standalone listener strips every `X-Continuum-*` header
(`httproutes/server.go:51`) and `/abs/api/login` requires
`X-Continuum-User-Id` to be set (`abs/handler.go:293`). The only documented
path for a listener to obtain an ABS access token is to first log into the
Continuum portal in a browser. The official ABS clients only POST
`{username, password}` to `/login` and have no UI for pasting a pre-minted
JWT, so the standalone port effectively has no working login flow for them.

## Goal

Let listeners log into the official Audiobookshelf clients with their
Continuum username and password directly against the standalone port, without
weakening the existing trust model for host-proxied callers.

## Non-goals

- Storing passwords inside the audiobooks plugin. Credential validation
  remains the Continuum host's job.
- A new SDK RPC. The host's existing public `POST /api/v1/auth/login` is
  sufficient; a typed RPC can come later if a second plugin needs the same.
- Changes to ebooks. OPDS already works with portal-issued tokens; revisit
  that surface separately once this lands.

## Design

### Validation path

The handler at `abs/handler.go:293` keeps its existing `X-Continuum-User-Id`
fast path. When that header is absent and admin mode is not `disabled`, the
handler:

1. Decodes the body — Audiobookshelf clients send
   `{"username": "...", "password": "..."}`.
2. Calls `hostlogin.Validate(ctx, username, password, deviceName, ip)`, which
   POSTs `{username, password, provider: "local"}` to
   `{CONTINUUM_HOST_URL}/api/v1/auth/login` and reads `{user.id, user.name}`
   from a 200 response. `provider: "local"` pins the host to its local
   provider so OIDC-only users without a local password fail closed rather
   than fall through to an unrelated provider.
3. On the host's 401/403, returns 401 with the ABS error shape.
4. On any other host failure, returns 502.
5. If admin mode is `opt_in`, looks up the validated user id in
   `abs_standalone_opt_ins`. Missing row returns 401 with
   `not_enabled_for_mobile_login`.
6. Mints access + refresh JWTs and returns the same response shape as the
   header path. Token rows go into `abs_tokens` exactly as today.

The host's `LocalProvider.Authenticate`
(`continuum/internal/auth/provider.go:55`) already gates on
`user.LocalPasswordLoginEnabled`, so listeners who exist only as OIDC users
without a local password automatically fail validation. Listeners with a
local password set (whether their normal sign-in is local or OIDC) succeed.

### Admin setting

New `backend_config.standalone_login_mode TEXT NOT NULL DEFAULT 'disabled'`
column. Enum values:

- `disabled` — body-creds path is off, behaviour is unchanged.
- `opt_in` — body-creds path is on, but each listener must enable it from
  their account settings before their client can log in.
- `all_accounts` — body-creds path is on for every account with a local
  password.

Exposed via the existing admin Settings page next to the standalone listen
address.

### Per-user opt-in storage

New table:

```
abs_standalone_opt_ins (
  user_id     TEXT PRIMARY KEY,
  enabled_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)
```

The user-facing portal SPA gets a "Allow mobile-app login" toggle backed by
two authenticated routes:

- `POST /api/v1/me/abs-standalone` — upsert a row for the calling user.
- `DELETE /api/v1/me/abs-standalone` — remove the row.
- `GET /api/v1/me/abs-standalone` — return `{enabled, mode}` so the SPA can
  show the toggle only when it is meaningful (hidden under `disabled`,
  hidden under `all_accounts`).

### Rate limiting

A new in-process per-IP token bucket on the standalone `/abs/api/login`
handler, mirroring `continuum-plugin-ebooks/internal/server/ratelimit.go`.
30 req/s / burst 60 by source IP. Only failed body-creds attempts count
against the bucket; successful logins and the header path do not. The host
endpoint has its own limiter as defence-in-depth.

### Audit

On every body-creds attempt the handler logs `abs.standalone_login` at info
on success and warn on failure with `{user_id?, ip, user_agent, result,
mode}`.

## Security review

- Header stripping at `httproutes/server.go:51` is unchanged. Identity from
  the standalone port is established only by credential validation, never by
  trusted-proxy headers.
- The handler refuses to validate when the host call returns a non-2xx other
  than 401/403, mapping to 502. A misconfigured `CONTINUUM_HOST_URL` cannot
  silently succeed.
- Disabling the toggle for a user revokes their ability to log in via the
  body-creds path but does not invalidate their existing ABS tokens. Token
  invalidation is the operator's responsibility via the existing admin
  token-revoke endpoint.

## Migration

`0011_standalone_abs_login.{up,down}.sql`:

- Adds `standalone_login_mode TEXT NOT NULL DEFAULT 'disabled'` to
  `backend_config`.
- Creates `abs_standalone_opt_ins`.

Down migration drops both.

## Tests

`internal/abs/handler_test.go`:

- Existing `TestHandleLogin_RejectsMissingIdentity` is updated for
  `disabled` mode.
- New cases for `opt_in` + no row → 401, `opt_in` + row → 200,
  `all_accounts` → 200, host 401 → 401, host 503 → 502, rate limiter
  triggers after burst exhaustion from one IP.
- `internal/hostlogin/client_test.go` exercises the host client against
  `httptest.Server` fixtures.

## Out of scope / follow-up

- An ebooks-side equivalent for OPDS Basic-Auth that accepts portal
  credentials instead of a portal-issued OPDS token. Same `hostlogin`
  helper, separate change.

### Standalone-listener audio streaming (open)

After login, ABS clients connected to the standalone listener can browse the
catalog and load covers (covers are now proxied as bytes — see
`internal/abs/handler.go:handleItemCover`). Audio playback, however, still
ends in a redirect to the backend plugin's stream URL — a path under
`/api/v1/plugins/<install>/api/v1/stream/...` that is only routable through
the Continuum host's plugin proxy. ABS clients connected to the standalone
listener (e.g. `abs.example.com`) cannot follow that redirect because their
origin doesn't serve the host's plugin-proxy path space.

Two ways to close the gap:

1. Proxy audio bytes through the standalone listener, mirroring the
   cover-bytes proxy. Requires a streaming variant of the host SDK's
   `CallPluginHTTP` (current shape buffers the whole body, capped at 10 MiB;
   useless for audiobook files). Heaviest cost on the plugin host —
   hundreds of MiB per playback session flow through the plugin.
2. Make the backend plugin's stream route reachable on its own public URL
   (operator deploys `bw-audio` / `local-audiobooks` behind their own
   reverse proxy and points the audiobooks plugin at that URL). Cheaper
   on the plugin host but requires extra operator config.

Until one of these is in place, audio playback works only when the ABS
client is reaching the plugin via the host proxy (uncommon — the host
proxy gates on a host session token that ABS clients don't have). The
host-proxied path will work end-to-end for any tooling that *does* have a
host session (e.g. the audiobooks portal's own SPA).
