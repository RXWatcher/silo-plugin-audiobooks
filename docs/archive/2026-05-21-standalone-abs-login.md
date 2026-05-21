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

### Realtime endpoint (Socket.io)

The plugin now exposes a Socket.io v4 endpoint at `/socket.io/*` on the
standalone listener (`internal/abssocket`). ABS clients connect, then
emit a Socket.io application event named `"auth"` whose payload is the
access JWT minted by `/abs/api/login`. The server validates the JWT
(signature + type + optional revocation lookup) and joins the
connection to a user-scoped room. Subsequent server-pushed events fire
to every device on the same account.

Wired publish points:

- `PATCH /abs/api/me/progress/{itemId}` → `user_item_progress_updated`
- `PATCH /abs/api/session/{sid}` → `user_item_progress_updated`
  (a thin payload — itemId + currentTime + sessionId — to avoid an
  extra DB round-trip in the playback hot path)
- `POST /abs/api/items/{id}/play` → `user_session_open`
- `POST /abs/api/session/{sid}/close` → `user_session_closed`

The Continuum host plugin proxy can't bridge websocket upgrades (the
SDK's `CallPluginHTTP` is request/response), so this endpoint is
reachable only over the standalone listener — which is also where the
official ABS mobile and web clients actually connect.

Process-scope hub by default. For multi-replica deployments, set
`CONTINUUM_REDIS_URL` (any URL accepted by `go-redis.ParseURL`) and the
plugin will wire `zishang520/socket.io-go-redis` as the Socket.io
adapter so events published on one replica reach clients connected to
another. An unparseable URL or unreachable Redis logs a warning and
falls back to the in-memory adapter rather than failing the boot — the
plugin stays functional as a single-replica deployment.

Backend-side catalog events (`audiobook_imported` on
`bookwarehouse-audio` / `audiobook-requests`) now fan out as Socket.io
`item_added` broadcasts via the consumer's `Broadcast` dependency, so
connected ABS clients refresh their library view without polling.

Filter pushdown: the audiobooks plugin's call to backend `ListCatalog`
now carries `filter=<kind>` and `filter_value=<decoded>` query
parameters when an ABS client filtered the request. Backends that
implement the contract apply an index hit; backends that don't are
documented to ignore the params (we still apply the filter locally on
the response).

### Tier 3 — audited and dispositioned

The original Tier 3 list was skipped on first pass because the booklore
documentation didn't pin down a spec for them. On a second-pass audit
each was either already covered, owned by another layer, or
deliberately deferred for lack of an integration target:

- **Refresh-token rotation**: present and correct.
  `/abs/api/auth/refresh` validates the inbound refresh token, mints a
  new pair, inserts both new JTIs, and revokes the old refresh JTI.
  Failure order is safe (step 4 before 5 means a mid-rotation crash
  leaves the client retriable on the old refresh). Concurrency note in
  the handler docstring: simultaneous double-refresh from one client
  is tolerated. No code changes needed.

- **CSRF protection**: owned by the continuum host. The host's portal
  session cookie carries `SameSite=Lax`
  (`continuum/internal/api/handlers/auth.go:349`), which blocks the
  cross-site state-changing POST attack vector at the front door.
  Plugin routes inherit the protection because the host's plugin
  proxy refuses to forward requests without a validated session.
  Plugin-level CSRF would be a duplicative defense layer; not added.

- **Push-notification registration**: deferred. Real ABS doesn't
  publish a stable spec for it, and implementing the feature requires
  an integration target (FCM, APNs, or a custom relay) that we don't
  currently have. Building a registration endpoint with no consumer
  would add maintenance surface for no benefit. Revisit when there is
  a specific client UX that needs it.

- **Sharing tokens / public-link tokens**: deferred for the same
  reason as push notifications. Without a documented ABS spec or a
  consumer UI, the shape of the share endpoint would be invented from
  scratch — and a future spec landing on different semantics would
  force a breaking change. Revisit if a real client asks for it.

- **Podcast/episode parallel schema**: out of scope for the audiobooks
  plugin. The ebooks plugin handles ebook + podcast flows separately;
  audiobook-only listeners aren't affected. A dedicated podcasts
  plugin would be the right home for this surface, not a parallel
  schema bolted onto audiobooks.

### Standalone-listener audio streaming

ABS clients connected to the standalone listener now stream audio directly
through the plugin: `handlePublicTrack` no longer redirects — it mints a
signed media token, opens a `GetStream` to the backend's
`/api/v1/stream/<id>/<idx>` route, and copies bytes back to the ABS client
with `Range` / `Content-Range` pass-through. The `streaming.Router`'s
redirect path is kept for the audiobook portal's own SPA, which lives on
the host origin where the redirect target is reachable.

Bandwidth cost is high (audiobook files flow through the plugin host) but
that's the trade-off for letting standalone-listener clients work without
extra operator config. A backend with its own public URL would still
short-circuit this if the operator wants to pin the bytes to a different
origin; we just don't require it.
