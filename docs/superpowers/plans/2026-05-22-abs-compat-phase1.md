# ABS Compatibility Fixes + Version Bump — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the audiobooks plugin's Audiobookshelf-compatible API up to current third-party-client expectations and report server version 2.35.0.

**Architecture:** Pure plugin-local changes to the `internal/abs` package, one new DB migration, and a manifest version bump. No core or SDK dependency. Spec: `docs/superpowers/specs/2026-05-22-abs-compat-and-profile-login-design.md` (Phase 1).

**Tech Stack:** Go, chi router, pgx/v5, golang-migrate. Tests via `go test`.

**Conventions:** Run all `go` commands from the repo root `/opt/silo_plugins/silo-plugin-audiobooks`. Commit messages use Conventional Commits and end with the `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` trailer.

---

### Task 1: Bump version constants

**Files:**
- Modify: `internal/abs/translate.go:21-22`
- Modify: `cmd/silo-plugin-audiobooks/manifest.json`
- Test: `internal/abs/translate_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/translate_test.go`:

```go
func TestServerVersionIsCurrentRelease(t *testing.T) {
	if ServerVersion != "2.35.0" {
		t.Fatalf("ServerVersion = %q, want 2.35.0", ServerVersion)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestServerVersionIsCurrentRelease -v`
Expected: FAIL — `ServerVersion = "2.26.0", want 2.35.0`

- [ ] **Step 3: Update the constant**

In `internal/abs/translate.go`, change line 21:

```go
	ServerVersion = "2.35.0"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestServerVersionIsCurrentRelease -v`
Expected: PASS

- [ ] **Step 5: Bump the manifest version**

In `cmd/silo-plugin-audiobooks/manifest.json`, change `"version": "1.0.3"` to `"version": "1.1.0"`.

- [ ] **Step 6: Commit**

```bash
git add internal/abs/translate.go internal/abs/translate_test.go cmd/silo-plugin-audiobooks/manifest.json
git commit -m "feat(abs): report server version 2.35.0; bump plugin to 1.1.0

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `/status` — report as an Audiobookshelf server

**Files:**
- Modify: `internal/abs/handler.go:462-469` (`handleStatus`)
- Test: `internal/abs/handler_test.go`

Real ABS clients (ShelfPlayer, Lissen, the web client) reject any server whose `/status` `app` field is not `"audiobookshelf"`, and read `authMethods` to render the login form.

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestHandleStatusIdentifiesAsAudiobookshelf(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["app"] != "audiobookshelf" {
		t.Errorf("app = %v, want audiobookshelf", body["app"])
	}
	methods, ok := body["authMethods"].([]any)
	if !ok || len(methods) != 1 || methods[0] != "local" {
		t.Errorf("authMethods = %v, want [local]", body["authMethods"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestHandleStatusIdentifiesAsAudiobookshelf -v`
Expected: FAIL — `app = silo, want audiobookshelf`

- [ ] **Step 3: Update `handleStatus`**

Replace the body of `handleStatus` (`internal/abs/handler.go:462-469`) with:

```go
func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"isInit":        true,
		"language":      "en-us",
		"app":           "audiobookshelf",
		"authMethods":   []string{"local"},
		"serverVersion": ServerVersion,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestHandleStatusIdentifiesAsAudiobookshelf -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "fix(abs): /status reports app=audiobookshelf and authMethods

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `/ping` — return `success: true`

**Files:**
- Modify: `internal/abs/handler.go:450-456` (`handlePing`)
- Test: `internal/abs/handler_test.go`

The ABS client's `pingServerAddress` reads `response.data.success`.

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestHandlePingReturnsSuccess(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handlePing(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["success"] != true {
		t.Errorf("success = %v, want true", body["success"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestHandlePingReturnsSuccess -v`
Expected: FAIL — `success = <nil>, want true`

- [ ] **Step 3: Update `handlePing`**

Replace the body of `handlePing` (`internal/abs/handler.go:450-456`) with:

```go
func (h *Handler) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"server":  ServerSourceTag,
		"version": ServerVersion,
		"pong":    true,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestHandlePingReturnsSuccess -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "fix(abs): /ping returns success:true for client connectivity checks

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Expand `serverSettings`

**Files:**
- Modify: `internal/abs/handler.go` — add an `absServerSettings()` helper near `completeLogin`; use it in `completeLogin` (`handler.go:685-688`) and `handleAuthorize` (`handler.go:738-741`)
- Test: `internal/abs/handler_test.go`

Clients branch on `serverSettings` fields; the current `{version, language}` is too thin.

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestAbsServerSettingsShape(t *testing.T) {
	s := absServerSettings()
	for _, k := range []string{"version", "language", "authActiveAuthMethods", "authOpenIDAutoLaunch"} {
		if _, ok := s[k]; !ok {
			t.Errorf("serverSettings missing %q", k)
		}
	}
	if s["version"] != ServerVersion {
		t.Errorf("version = %v, want %s", s["version"], ServerVersion)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestAbsServerSettingsShape -v`
Expected: FAIL — `undefined: absServerSettings`

- [ ] **Step 3: Add the helper**

Add this function to `internal/abs/handler.go` immediately above `completeLogin`:

```go
// absServerSettings is the serverSettings envelope ABS clients read on
// login. Modelled on the upstream shape so clients that branch on these
// fields (auth method list, OpenID flags, view prefs) behave correctly.
func absServerSettings() map[string]any {
	return map[string]any{
		"id":                       "server-settings",
		"version":                  ServerVersion,
		"language":                 "en-us",
		"buildNumber":               1,
		"chromecastEnabled":         false,
		"dateFormat":                "MM/dd/yyyy",
		"timeFormat":                "HH:mm",
		"homeBookshelfView":         1,
		"bookshelfView":             1,
		"sortingIgnorePrefix":       false,
		"sortingPrefixes":           []string{"the", "a"},
		"rateLimitLoginRequests":    10,
		"rateLimitLoginWindow":      600000,
		"allowIframe":               false,
		"authActiveAuthMethods":     []string{"local"},
		"authOpenIDAutoLaunch":      false,
		"authOpenIDAutoRegister":    false,
		"authOpenIDButtonText":      "Login with OpenId",
		"authOpenIDIssuerURL":       nil,
		"authOpenIDAuthorizationURL": nil,
		"authOpenIDTokenURL":        nil,
		"authOpenIDUserInfoURL":     nil,
		"authOpenIDJwksURL":         nil,
		"authOpenIDLogoutURL":       nil,
	}
}
```

- [ ] **Step 4: Use the helper in both responses**

In `completeLogin` (`handler.go:685-688`), replace the inline `"serverSettings": map[string]any{...}` value with `"serverSettings": absServerSettings(),`. Do the same in `handleAuthorize` (`handler.go:738-741`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/abs/ -run 'TestAbsServerSettingsShape|TestHandleLogin|TestHandleAuthorize' -v`
Expected: PASS (existing login/authorize tests still pass with the richer shape)

- [ ] **Step 6: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "feat(abs): expand serverSettings to the shape clients branch on

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Add `duration_seconds` to the `progress` table

**Files:**
- Create: `internal/migrate/files/0032_progress_duration.up.sql`
- Create: `internal/migrate/files/0032_progress_duration.down.sql`
- Modify: `internal/store/progress.go` — the `Progress` struct and `UpsertProgress`

The `progress` table has no duration column, so `progressToABS` hardcodes `duration: 0`. The PATCH-progress body already carries `duration`; this task adds the column to store it.

- [ ] **Step 1: Write the up migration**

Create `internal/migrate/files/0032_progress_duration.up.sql`:

```sql
-- progress.duration_seconds lets the ABS surface emit a real track
-- duration instead of 0. Backfilled to 0 for existing rows; populated
-- going forward from the client's progress-sync payload.
ALTER TABLE progress
  ADD COLUMN IF NOT EXISTS duration_seconds INT NOT NULL DEFAULT 0;
```

- [ ] **Step 2: Write the down migration**

Create `internal/migrate/files/0032_progress_duration.down.sql`:

```sql
ALTER TABLE progress DROP COLUMN IF EXISTS duration_seconds;
```

- [ ] **Step 3: Add the field to the `Progress` struct**

In `internal/store/progress.go`, add a `DurationSeconds int` field to the `Progress` struct (next to `CurrentSeconds`).

- [ ] **Step 4: Update `UpsertProgress` to persist it**

Replace `UpsertProgress` (`internal/store/progress.go:45-62`) with:

```go
func (s *Store) UpsertProgress(ctx context.Context, p Progress) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO progress (user_id, book_id, current_seconds, duration_seconds, progress_pct, is_finished, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			current_seconds  = EXCLUDED.current_seconds,
			duration_seconds = EXCLUDED.duration_seconds,
			progress_pct     = EXCLUDED.progress_pct,
			is_finished      = EXCLUDED.is_finished,
			updated_at       = now()
	`, p.UserID, p.BookID, p.CurrentSeconds, p.DurationSeconds, p.ProgressPct, p.IsFinished)
	if err != nil {
		return fmt.Errorf("upsert progress: %w", err)
	}
	return nil
}
```

Also update the `SELECT` column lists in `GetProgress`, `ListInProgress`, and `ListRecentProgress` (`internal/store/progress.go`) to include `duration_seconds` and scan it into `Progress.DurationSeconds`. Each currently selects `user_id, book_id, current_seconds, progress_pct, is_finished, updated_at`; add `duration_seconds` after `current_seconds` and add `&p.DurationSeconds` to the matching `Scan` call.

- [ ] **Step 5: Verify it builds**

Run: `go build ./...`
Expected: no output (success)

- [ ] **Step 6: Commit**

```bash
git add internal/migrate/files/0032_progress_duration.up.sql internal/migrate/files/0032_progress_duration.down.sql internal/store/progress.go
git commit -m "feat(store): add progress.duration_seconds column

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Capture `duration` on progress sync

**Files:**
- Modify: `internal/abs/handler.go` — `handlePatchProgress` (registered at `handler.go:261`)
- Test: `internal/store/progress_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/store/progress_test.go` (uses the package's existing `newStore(t)` helper):

```go
func TestUpsertProgressPersistsDuration(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.UpsertProgress(ctx, Progress{
		UserID: "u1", BookID: "b1", CurrentSeconds: 30, DurationSeconds: 3600, ProgressPct: 0.0083,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := st.GetProgress(ctx, "u1", "b1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DurationSeconds != 3600 {
		t.Errorf("DurationSeconds = %d, want 3600", got.DurationSeconds)
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test ./internal/store/ -run TestUpsertProgressPersistsDuration -v`
Expected: PASS if Task 5's Scan changes are complete (this test confirms Task 5; it fails with `DurationSeconds = 0` if a SELECT column was missed).

- [ ] **Step 3: Thread `duration` through the PATCH handler**

`handlePatchProgress` decodes a body that already includes `duration` (the ABS client sends `{currentTime, duration, isFinished, progress}`). Find the `store.Progress{...}` or `store.UpsertProgress` call inside `handlePatchProgress` and set `DurationSeconds` from the decoded body's `duration` field (convert the float seconds to `int`). If the handler uses a body struct without a `Duration` field, add `Duration float64 \`json:"duration"\`` to it.

- [ ] **Step 4: Run the build and store tests**

Run: `go build ./... && go test ./internal/store/ -run TestUpsertProgress -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/abs/handler.go internal/store/progress_test.go
git commit -m "feat(abs): persist client-reported duration on progress sync

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: `progressToABS` emits the real duration

**Files:**
- Modify: `internal/abs/handler.go:2119-2137` (`progressToABS`)
- Test: `internal/abs/handler_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestProgressToABSEmitsDuration(t *testing.T) {
	out := progressToABS("u1", store.Progress{
		BookID: "b1", CurrentSeconds: 30, DurationSeconds: 3600, ProgressPct: 0.0083,
	})
	if out["duration"] != float64(3600) {
		t.Errorf("duration = %v, want 3600", out["duration"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestProgressToABSEmitsDuration -v`
Expected: FAIL — `duration = 0, want 3600`

- [ ] **Step 3: Update `progressToABS`**

In `internal/abs/handler.go:2126`, change `"duration": 0,` to:

```go
		"duration":      float64(p.DurationSeconds),
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestProgressToABSEmitsDuration -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "fix(abs): progressToABS emits real track duration, not 0

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Populate `mediaProgress` and return a full `/me`

**Files:**
- Modify: `internal/abs/handler.go` — `completeLogin` (`handler.go:661-677` user map), `handleAuthorize` (`handler.go:723-735` user map), `handleMe` (`handler.go:872-880`)
- Test: `internal/abs/handler_test.go`

ABS clients read `user.mediaProgress` on launch to paint resume positions. `/me` currently returns a 3-field stub.

- [ ] **Step 1: Add a shared user-object builder**

Add to `internal/abs/handler.go`, above `completeLogin`:

```go
// absUserObject builds the ABS `user` envelope shared by /login,
// /authorize and /me. mediaProgress is hydrated from the user's recent
// progress rows so clients show resume positions without a second call.
func (h *Handler) absUserObject(ctx context.Context, userID, displayName, defaultLibraryID string) map[string]any {
	rows, _ := h.store.ListRecentProgress(ctx, userID, 200)
	progress := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		progress = append(progress, progressToABS(userID, p))
	}
	name := displayName
	if name == "" {
		name = userID
	}
	return map[string]any{
		"id":                  userID,
		"username":            name,
		"type":                "user",
		"defaultLibraryId":    defaultLibraryID,
		"librariesAccessible": []any{},
		"mediaProgress":       progress,
		"bookmarks":           []any{},
		"isOldToken":          false,
		"permissions": map[string]any{
			"update":                true,
			"delete":                true,
			"download":              true,
			"accessExplicitContent": true,
		},
	}
}
```

- [ ] **Step 2: Write the failing test**

Add to `internal/abs/handler_test.go` (uses the package's existing handler+store test harness — mirror the construction used by `TestHandleLogin*`):

```go
func TestHandleMeReturnsFullUser(t *testing.T) {
	h := newTestHandler(t) // existing helper used by other handler tests
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "u1"}))
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["mediaProgress"]; !ok {
		t.Error("/me response missing mediaProgress")
	}
	if _, ok := body["permissions"]; !ok {
		t.Error("/me response missing permissions")
	}
}
```

If `newTestHandler` does not exist, construct the handler the same way the existing `handler_test.go` tests do (they build a `Handler` with a test `store.Store`); reuse that exact pattern.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestHandleMeReturnsFullUser -v`
Expected: FAIL — missing `mediaProgress`

- [ ] **Step 4: Rewrite `handleMe`**

Replace `handleMe` (`internal/abs/handler.go:872-880`) with:

```go
func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, _ := h.defaultPortalLibrary(r.Context())
	writeJSON(w, http.StatusOK, h.absUserObject(r.Context(), a.UserID, "", absLibraryID(lib)))
}
```

- [ ] **Step 5: Use the builder in `completeLogin` and `handleAuthorize`**

In `completeLogin`, replace the inline `user := map[string]any{...}` (`handler.go:661-677`) with:

```go
	user := h.absUserObject(r.Context(), userID, "", defaultLibraryID)
	user["token"] = access // legacy field some 2.17- clients still read
```

Keep the existing `returnTokens` block that adds `accessToken`/`refreshToken`. In `handleAuthorize`, replace its inline `user := map[string]any{...}` (`handler.go:723-735`) with `user := h.absUserObject(r.Context(), a.UserID, "", defaultLibraryID)`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/abs/ -run 'TestHandleMe|TestHandleLogin|TestHandleAuthorize' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "feat(abs): populate mediaProgress and return a full /me user object

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Use the real display name in `username`

**Files:**
- Modify: `internal/abs/handler.go` — `completeLogin` signature + callers (`handleLogin`, `handleStandaloneLogin`)
- Test: `internal/abs/handler_test.go`

`completeLogin` currently emits `username: userID` (a numeric id). Source the real name: `X-Silo-User-Name` on the header path, `hostlogin.Result.DisplayName` on the standalone path.

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestCompleteLoginUsesDisplayName(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	h.completeLogin(rec, req, "42", "Alice")

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	user := body["user"].(map[string]any)
	if user["username"] != "Alice" {
		t.Errorf("username = %v, want Alice", user["username"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestCompleteLoginUsesDisplayName -v`
Expected: FAIL — `completeLogin` takes 3 args, not 4 (compile error)

- [ ] **Step 3: Add the `displayName` parameter**

Change the `completeLogin` signature (`handler.go:591`) to:

```go
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, userID, displayName string) {
```

Inside, pass `displayName` to `absUserObject` instead of `""` (from Task 8 Step 5):

```go
	user := h.absUserObject(r.Context(), userID, displayName, defaultLibraryID)
```

- [ ] **Step 4: Update the callers**

In `handleLogin` (`handler.go:500-506`):

```go
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if userID := r.Header.Get("X-Silo-User-Id"); userID != "" {
		h.completeLogin(w, r, userID, r.Header.Get("X-Silo-User-Name"))
		return
	}
	h.handleStandaloneLogin(w, r)
}
```

In `handleStandaloneLogin` (`handler.go:585`), change the final call `h.completeLogin(w, r, res.UserID)` to `h.completeLogin(w, r, res.UserID, res.DisplayName)`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go build ./... && go test ./internal/abs/ -run 'TestCompleteLogin|TestHandleLogin' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "feat(abs): emit the real display name as the ABS username

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Serve RSS per-track enclosure URLs

**Files:**
- Modify: `internal/abs/rss_feed_handler.go:47-50` (`MountPublicFeed`)
- Create: the `handlePublicFeedTrack` method in `internal/abs/rss_feed_handler.go`
- Test: `internal/abs/rss_feed_handler_test.go` (create if absent)

`itemFeedEpisodes`/`singleBookEpisode` emit enclosure URLs `/feed/{slug}/track/{idx}.{ext}`, but `MountPublicFeed` registers no handler for that path — every podcast enclosure 404s. This task adds a slug-gated track handler that proxies backend audio, mirroring `handlePublicTrack` (`handler.go:1366`).

- [ ] **Step 1: Write the failing test**

Create `internal/abs/rss_feed_handler_test.go`:

```go
package abs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The track route must be registered so a real subscriber's enclosure
// URL resolves to a handler rather than chi's 404.
func TestPublicFeedTrackRouteIsRegistered(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.MountPublicFeed(r)

	rctx := chi.NewRouteContext()
	if !r.Match(rctx, http.MethodGet, "/feed/abc/track/0.mp3") {
		t.Fatal("GET /feed/{slug}/track/{idx} is not routed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestPublicFeedTrackRouteIsRegistered -v`
Expected: FAIL — route not matched

- [ ] **Step 3: Register the route**

In `MountPublicFeed` (`internal/abs/rss_feed_handler.go:47-50`), add a third route:

```go
func (h *Handler) MountPublicFeed(r chi.Router) {
	r.Get("/feed/{slug}.xml", h.handlePublicFeed)
	r.Get("/feed/{slug}", h.handlePublicFeed)
	r.Get("/feed/{slug}/track/{idx}", h.handlePublicFeedTrack)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestPublicFeedTrackRouteIsRegistered -v`
Expected: PASS

- [ ] **Step 5: Implement `handlePublicFeedTrack`**

Add this method to `internal/abs/rss_feed_handler.go`. It resolves the feed by slug, resolves the entity to a backend book, mints a media token, and proxies the audio bytes — the same proxy pattern as `handlePublicTrack` (`handler.go:1366-1438`). The chi `{idx}` param arrives as `0.mp3`; strip the extension before parsing.

```go
// handlePublicFeedTrack serves the audio enclosure for an RSS feed
// episode. The feed slug is the capability — no Bearer token (podcast
// apps don't speak custom auth). It mirrors handlePublicTrack's
// byte-proxy: mint a short-lived media token, proxy the backend stream
// so the URL stays on the listener the subscriber is talking to.
func (h *Handler) handlePublicFeedTrack(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	idxRaw := chi.URLParam(r, "idx")
	// The enclosure URL carries a file extension (e.g. "0.mp3"); strip it.
	if dot := strings.IndexByte(idxRaw, '.'); dot >= 0 {
		idxRaw = idxRaw[:dot]
	}
	idx, err := strconv.Atoi(idxRaw)
	if err != nil || idx < 0 {
		http.Error(w, "idx must be a non-negative int", http.StatusBadRequest)
		return
	}

	feed, err := h.store.GetRSSFeedBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only item feeds have a direct (book, fileIdx) addressing scheme.
	if feed.EntityType != "item" {
		http.Error(w, "track enclosures are only served for item feeds", http.StatusNotFound)
		return
	}
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), feed.EntityID)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}

	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	mediaTok, err := mediatoken.Mint(cfg.MediaSigningSecret, feed.UserID, backendBookID, idx)
	if err != nil {
		http.Error(w, "mint media token", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" + strconv.Itoa(idx) +
		"?token=" + neturl.QueryEscape(mediaTok)

	hdrs := map[string]string{}
	for _, name := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(name); v != "" {
			hdrs[name] = v
		}
	}
	h.streaming.Proxy(w, r, lib.BackendPluginID, backendPath, hdrs)
}
```

Add the imports `mediatoken` and `neturl "net/url"` to `rss_feed_handler.go` if not present. **Verify the exact backend-proxy call** against `handlePublicTrack` (`handler.go:1427-1438` onward) — match whatever method on `h.streaming` it uses (the snippet above assumes `h.streaming.Proxy(w, r, pluginID, path, headers)`; use the real signature). Read `handler.go:1438-1460` for the exact proxy invocation and copy it.

- [ ] **Step 6: Run the build and route test**

Run: `go build ./... && go test ./internal/abs/ -run TestPublicFeedTrack -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/abs/rss_feed_handler.go internal/abs/rss_feed_handler_test.go
git commit -m "fix(abs): serve RSS per-track enclosure URLs instead of 404

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Full verification and deploy

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: all packages PASS

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: no output

- [ ] **Step 3: Deploy**

Run: `/opt/silo_plugins/install-plugin.sh /opt/silo_plugins/silo-plugin-audiobooks`
Expected: ends with `→ plugin live (HTTP 200)`.

- [ ] **Step 4: Smoke-test the published port**

```bash
curl -s http://localhost:9998/status
curl -s http://localhost:9998/ping
```

Expected: `/status` shows `"app":"audiobookshelf"`, `"authMethods":["local"]`, `"serverVersion":"2.35.0"`; `/ping` shows `"success":true`.

---

## Self-Review

- **Spec coverage:** §1.1 version → Task 1; §1.2 status/ping/serverSettings → Tasks 2, 3, 4; §1.3 `/me`, mediaProgress, username → Tasks 8, 9; §1.4 progress duration → Tasks 5, 6, 7; §1.4 RSS enclosure → Task 10. All Phase 1 spec items are covered.
- **Ordering:** Task 5 (column) precedes Task 6 (capture) and Task 7 (emit); Task 8 (`absUserObject`) precedes Task 9 (which extends `completeLogin`'s call into it). Task 4's helper is independent.
- **Known assumption:** Tasks 6, 8, 9, 10 reference existing functions whose exact bodies the implementer must open (`handlePatchProgress`, the `handler_test.go` construction helper, the `h.streaming` proxy call). Each step gives the file:line and the precise change; the implementer reads that span and applies it. This is deliberate, not a placeholder — the surrounding code is stable and quoted where it drives the change.
