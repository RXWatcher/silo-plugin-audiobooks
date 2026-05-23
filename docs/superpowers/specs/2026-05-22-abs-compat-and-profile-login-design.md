# Audiobooks Plugin — ABS Compatibility Fixes + Profile-Aware Auth — Design

**Status:** Draft 2026-05-22. Plan to follow.

**Goal:** Two independently shippable phases.

- **Phase 1** brings the plugin's Audiobookshelf-compatible API up to what
  current third-party clients expect — response-shape corrections and a
  version bump to 2.35.0. No dependencies; ships on its own.
- **Phase 2** makes the plugin profile-aware. A third-party ABS client logs in
  as `user#profile`; the web portal scopes to the active profile; every
  per-listener object (collections, smart-collections, playlists, progress,
  bookmarks, continue-listening, sessions) is keyed by profile.

## Prerequisite (Phase 2 only)

Phase 2 consumes the approved core design
`/opt/silo/docs/superpowers/specs/2026-05-13-profile-aware-third-party-auth-design.md`:
the `RuntimeHost.ValidateProfileCredential` RPC and the `X-Silo-Profile-Id`
proxy header. These are built and deployed on core branch `feat/plugin-patches`,
including the primary-profile normalization (core commit `12ef0e4a`) so that
`profile_id = ""` canonically means the primary profile on both the RPC and the
proxy header. Phase 1 has no dependency and may ship before the core branch
merges.

The plugin consumes the RPC via the SDK helper
`runtimehost.Client.ValidateProfileCredential(ctx, username, password)`, which
returns `{UserID, ProfileID}`. Until the SDK releases the RPC, the plugin's
`go.mod` carries a dev `replace` to the local SDK checkout (mirrors what core
already does).

---

## Phase 1 — ABS compatibility + version bump

Ships as plugin v1.1.0. Independent of profiles.

### 1.1 Version

- `internal/abs/translate.go`: `ServerVersion` `2.26.0` → `2.35.0` (current
  Audiobookshelf release). Surfaced by `/status`, `/ping`, and the
  `serverSettings` block of login responses.
- `cmd/silo-plugin-audiobooks/manifest.json`: plugin version
  `1.0.3` → `1.1.0`.

### 1.2 Handshake and identity responses

- `/status` (`handleStatus`): `app` field `"silo"` → `"audiobookshelf"`.
  Strict clients (ShelfPlayer, Lissen, the ABS web client) reject any other
  value; the official mobile app currently tolerates it but the check is a
  documented TODO upstream. Add `authMethods: ["local"]`.
- `/ping` (`handlePing`): add `success: true` — the ABS client's
  `pingServerAddress` reads `response.data.success`.
- `serverSettings` (`completeLogin`, `handleAuthorize`): expand from
  `{version, language}` to the fuller object real clients branch on — version,
  language, the `authOpenID*` fields (null / false), view preferences, and the
  rate-limit advertisement. Modelled on BookLore's `/login` `serverSettings`.

### 1.3 User payloads

- `/me` (`handleMe`): return the full user object, not the
  `{id, username, defaultLibraryId}` stub.
- `/login`, `/authorize`, `/me`: populate `mediaProgress` and `bookmarks` so
  clients paint resume positions on launch instead of waiting for a separate
  call.
- `username` field: emit the real display name, not the numeric host user id.
  Phase 1 sources it from the host-login result; Phase 2 re-points it at the
  resolved profile name.

### 1.4 Bug fixes

- `progressToABS`: stop emitting `duration: 0`; carry the real track duration
  so clients computing `currentTime / duration` do not divide by zero.
- RSS feeds: add the `/feed/{slug}/track/{idx}` route handler in
  `MountPublicFeed`. Feed XML already emits those per-track enclosure URLs but
  no handler serves them — they 404 today.

---

## Phase 2 — profile-aware audiobooks

Ships as plugin v1.2.0. Depends on the Phase-2 prerequisite above.

### 2.1 Login — two paths

`handleLogin` keeps two paths:

- **Path A — host-proxied.** Read `X-Silo-Profile-Id` (empty = primary)
  alongside `X-Silo-User-Id`; mint the ABS JWT carrying both.
- **Path B — standalone port.** `handleStandaloneLogin` stops calling the
  `hostlogin` HTTP client and instead calls
  `RuntimeHost.ValidateProfileCredential` with the raw `{username, password}`
  from the request body. The plugin never parses `#` — the host owns all
  parsing. `codes.Unauthenticated` → HTTP 401 (ABS "invalid username or
  password"); `codes.Unimplemented` / `codes.Unavailable` → HTTP 503.

The `internal/hostlogin` package and the `HostLoginValidator` interface are
removed — superseded by the RPC.

### 2.2 `standalone_login_mode` collapse

`standalone_login_mode` collapses from three values
(`disabled` / `opt_in` / `all_accounts`) to a plain operator on/off
(`enabled` / `disabled`). The `abs_standalone_opt_ins` table and the
`/me/abs-standalone` opt-in endpoints are dropped — the core profile + PIN
model supersedes per-user opt-in. The per-IP login rate limiter stays. A
migration maps existing `opt_in` / `all_accounts` → `enabled`,
`disabled` → `disabled`.

### 2.3 JWT `profile_id` claim

- `internal/abs/jwt.go`: add an optional `profile_id` claim (empty = primary).
- `completeLogin`: stamp `profile_id` into the access and refresh tokens.
- `bearerAuth`: resolve every authenticated request to `(userID, profileID)`.

### 2.4 Per-profile data re-keying

A schema migration adds `profile_id text NOT NULL DEFAULT ''` to every
user-owned table: collections, smart-collections, playlists, progress,
bookmarks, continue-listening flags, `abs_sessions`, `abs_tokens`. Existing
rows backfill to `''` (primary). Every scoping query gains
`AND profile_id = $n`. Uniqueness constraints that currently include
`user_id` extend to include `profile_id`.

### 2.5 Web SPA profile-awareness

The plugin builds no profile-management UI and no switcher — the core app owns
profile creation and selection. The audiobooks SPA becomes profile-aware: it
sends `X-Profile-Id` (the active profile chosen in the core app) on its API
calls; the host proxy stamps `X-Silo-Profile-Id`; the plugin scopes every
response to that profile. Switching profile in the core app re-scopes the
audiobooks UI — "in as Jim, switched to Laura" yields Laura's collections and
progress.

### 2.6 Display name

`ValidateProfileCredential` returns only `{user_id, profile_id}` — no name —
and Phase 2 removes `hostlogin`, so the ABS `username` field
(`/login`, `/authorize`, `/me`) is sourced per path:

- **Path A:** the `X-Silo-User-Name` / `X-Silo-Profile-Name` headers
  the host already stamps.
- **Path B:** the profile portion of the username string the client typed at
  login, captured onto the `abs_tokens` row at mint time so later token-only
  requests (`/me`, `/authorize`) return it without a name lookup.

This keeps the Phase 1 "real display name, not a numeric id" improvement
intact on the standalone path. No core change is needed — the typed username
is already in hand at login.

### 2.7 Migration safety

No existing row or token is lost. Every existing user-owned row maps to the
primary profile (`profile_id = ''`). A user who never creates a second profile
sees no behavioural change.

---

## Files touched (indicative; the plan pins exact locations)

| File | Phase | Change |
|---|---|---|
| `internal/abs/translate.go` | 1 | `ServerVersion` → 2.35.0 |
| `cmd/.../manifest.json` | 1, 2 | version 1.0.3 → 1.1.0 → 1.2.0 |
| `internal/abs/handler.go` | 1, 2 | `/status`, `/ping`, `/me`, `serverSettings`, login paths, `bearerAuth` |
| `internal/abs/handler.go` (`progressToABS`) | 1 | carry real duration |
| `internal/abs/rss_feed_handler.go` | 1 | `/feed/{slug}/track/{idx}` handler |
| `internal/abs/jwt.go` | 2 | `profile_id` claim |
| `internal/hostlogin/` | 2 | removed |
| `internal/server/abs_standalone.go` | 2 | drop opt-in endpoints |
| `internal/store/backend_config.go` | 2 | `standalone_login_mode` → enabled/disabled |
| `internal/store/*` (user-owned tables) | 2 | `profile_id` column + query scoping |
| `internal/migrate/` | 2 | `profile_id` migration + `standalone_login_mode` collapse |
| `cmd/silo-plugin-audiobooks/main.go` | 2 | wire the `runtimehost` client |
| `web/src/*` | 2 | send `X-Profile-Id`, profile-aware scoping |

## Test strategy

- Phase 1: handler tests asserting the corrected `/status`, `/ping`,
  `serverSettings`, `/me`, and populated `mediaProgress` shapes; a
  `progressToABS` duration test; an RSS track-enclosure route test.
- Phase 2: handler tests for Path B (`ValidateProfileCredential` faked,
  asserting the `profile_id` JWT claim and 401/503 error mapping);
  store tests asserting per-profile isolation (one user's two profiles do not
  see each other's collections/progress/bookmarks); a migration test asserting
  existing rows land on `profile_id = ''`.

## Out of scope

- Per-profile content / age restrictions — explicitly dropped.
- Plugin-side profile management UI — the core app owns profile lifecycle.
- Positive per-profile curation beyond what collections already provide.

## Open questions

None at draft. The primary-profile-identity ambiguity is resolved upstream
(core commit `12ef0e4a`): `profile_id = ""` is the single canonical primary
identifier on both the RPC and the proxy header.
