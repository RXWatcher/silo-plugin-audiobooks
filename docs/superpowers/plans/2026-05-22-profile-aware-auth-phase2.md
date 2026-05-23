# Profile-Aware Auth — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the audiobooks plugin profile-aware — a third-party ABS client logs in as `user#profile`, the web portal scopes to the active profile, and every per-listener table is keyed by profile.

**Architecture:** Login resolves a `(userID, profileID)` pair — Path A from the `X-Silo-Profile-Id` header, Path B from the core `RuntimeHost.ValidateProfileCredential` RPC. `profileID` rides in the ABS JWT, is resolved by `bearerAuth` into `ctxAuth`, and is threaded into every user-owned store query. Empty `profileID` (`""`) is the canonical primary profile. Spec: `docs/superpowers/specs/2026-05-22-abs-compat-and-profile-login-design.md` (Phase 2).

**Prerequisite — MUST be merged/deployed before this plan runs:** the core spec `2026-05-13-profile-aware-third-party-auth-design.md` — the `ValidateProfileCredential` RPC, the SDK helper `runtimehost.Client.ValidateProfileCredential`, the `X-Silo-Profile-Id` proxy header, and the primary-profile normalization (core commit `12ef0e4a`). Verify with: `grep -r ValidateProfileCredential /opt/silo_plugins/continuum-plugin-sdk/pkg` returns the helper.

**Tech Stack:** Go, chi, pgx/v5, golang-migrate, gRPC (RuntimeHost). Web: React 19 + TypeScript + Vite. Tests via `go test` / `vitest`.

**Conventions:** Run `go` commands from `/opt/silo_plugins/silo-plugin-audiobooks`. Conventional Commits; end messages with `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.

**Design note — composite key:** `profileID = ""` (primary) is NOT unique across users, so every re-keyed table keeps `user_id` and ADDS `profile_id`; scoping is always `WHERE user_id = $u AND profile_id = $p`. The `progress` primary key becomes `(user_id, profile_id, book_id)`.

---

### Task 1: Point the plugin's go.mod at the SDK with the RPC

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Confirm the SDK helper exists**

Run: `ls /opt/silo_plugins/continuum-plugin-sdk/pkg/pluginsdk/runtimehost/validate_profile_credential.go`
Expected: the file exists. If not, STOP — the prerequisite is not met.

- [ ] **Step 2: Add the dev replace directive**

Append to `go.mod`:

```
// Local dev: build against the in-tree SDK checkout until the
// ValidateProfileCredential RPC is released.
replace github.com/ContinuumApp/continuum-plugin-sdk => /opt/silo_plugins/continuum-plugin-sdk
```

- [ ] **Step 3: Tidy and verify**

Run: `go mod tidy && go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "build: point go.mod at the local SDK for ValidateProfileCredential

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Add the `profile_id` JWT claim

**Files:**
- Modify: `internal/abs/jwt.go`
- Test: `internal/abs/jwt_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/jwt_test.go`:

```go
func TestAccessTokenCarriesProfileID(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-123")
	tok, err := IssueAccessToken(secret, "u1", "p1", "jti1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.ProfileID != "p1" {
		t.Errorf("ProfileID = %q, want p1", claims.ProfileID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestAccessTokenCarriesProfileID -v`
Expected: FAIL — compile error (`IssueAccessToken` takes 4 args; `claims.ProfileID` undefined).

- [ ] **Step 3: Add the claim field**

In `internal/abs/jwt.go`, add to the `Claims` struct after `UserID`:

```go
	ProfileID string `json:"pid,omitempty"` // empty = primary profile
```

- [ ] **Step 4: Thread `profileID` through the issuers**

Change `IssueAccessToken` and `IssueRefreshToken` to take a `profileID string` parameter (insert it after `userID`), and set `ProfileID: profileID` in the `Claims` literal each builds. The shared `issue` helper (if present) gains the same parameter. `IssueSessionToken` does not need it — session tokens are book/file-scoped capabilities, not identities; leave it unchanged.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/abs/ -run TestAccessTokenCarriesProfileID -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/abs/jwt.go internal/abs/jwt_test.go
git commit -m "feat(abs): add profile_id claim to ABS access/refresh tokens

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Resolve `profileID` in `bearerAuth`

**Files:**
- Modify: `internal/abs/handler.go:292-296` (`ctxAuth`), `handler.go:325-329` (`bearerAuth` context value)
- Test: `internal/abs/handler_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/abs/handler_test.go`:

```go
func TestCtxAuthCarriesProfileID(t *testing.T) {
	a := ctxAuth{UserID: "u1", ProfileID: "p1"}
	if a.ProfileID != "p1" {
		t.Fatalf("ProfileID field missing or wrong")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestCtxAuthCarriesProfileID -v`
Expected: FAIL — `ctxAuth` has no `ProfileID` field.

- [ ] **Step 3: Add `ProfileID` to `ctxAuth`**

In `internal/abs/handler.go:292-296`:

```go
type ctxAuth struct {
	UserID    string
	ProfileID string
	JTI       string
	Token     string
}
```

- [ ] **Step 4: Populate it in `bearerAuth`**

In `bearerAuth` (`handler.go:325-329`), add `ProfileID` to the stored `ctxAuth`:

```go
		ctx := context.WithValue(r.Context(), ctxKey{}, ctxAuth{
			UserID:    claims.UserID,
			ProfileID: claims.ProfileID,
			JTI:       claims.JTI,
			Token:     raw,
		})
```

- [ ] **Step 5: Run test + build**

Run: `go build ./... && go test ./internal/abs/ -run TestCtxAuthCarriesProfileID -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "feat(abs): carry profile_id from JWT into request context

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Collapse `standalone_login_mode` to enabled/disabled

**Files:**
- Modify: `internal/store/backend_config.go:14-31`
- Create: `internal/migrate/files/0033_standalone_mode_collapse.up.sql` / `.down.sql`
- Test: `internal/store/backend_config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/store/backend_config_test.go`:

```go
func TestNormalizeStandaloneLoginModeCollapsed(t *testing.T) {
	cases := map[string]string{
		"opt_in":       StandaloneLoginModeEnabled,
		"all_accounts": StandaloneLoginModeEnabled,
		"enabled":      StandaloneLoginModeEnabled,
		"disabled":     StandaloneLoginModeDisabled,
		"":             StandaloneLoginModeDisabled,
	}
	for in, want := range cases {
		if got := NormalizeStandaloneLoginMode(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestNormalizeStandaloneLoginModeCollapsed -v`
Expected: FAIL — `StandaloneLoginModeEnabled` undefined.

- [ ] **Step 3: Replace the constants and normalizer**

Replace `internal/store/backend_config.go:14-31` with:

```go
// StandaloneLoginMode is the operator on/off switch for the standalone-port
// /abs/api/login body-creds path.
const (
	StandaloneLoginModeDisabled = "disabled"
	StandaloneLoginModeEnabled  = "enabled"
)

// NormalizeStandaloneLoginMode coerces any truthy legacy value
// (enabled / opt_in / all_accounts) to "enabled", everything else to
// "disabled". opt_in / all_accounts are pre-profile legacy values.
func NormalizeStandaloneLoginMode(v string) string {
	switch v {
	case StandaloneLoginModeEnabled, "opt_in", "all_accounts":
		return StandaloneLoginModeEnabled
	default:
		return StandaloneLoginModeDisabled
	}
}
```

- [ ] **Step 4: Write the migration**

Create `internal/migrate/files/0033_standalone_mode_collapse.up.sql`:

```sql
-- Collapse the three-mode standalone_login_mode to enabled/disabled.
UPDATE backend_config
   SET standalone_login_mode = 'enabled'
 WHERE standalone_login_mode IN ('opt_in', 'all_accounts');
```

Create `internal/migrate/files/0033_standalone_mode_collapse.down.sql`:

```sql
-- Irreversible value collapse; the down migration is a no-op.
SELECT 1;
```

- [ ] **Step 5: Run test + build**

Run: `go build ./... && go test ./internal/store/ -run TestNormalizeStandaloneLoginMode -v`
Expected: PASS. The compiler will flag remaining references to `StandaloneLoginModeOptIn` / `StandaloneLoginModeAllAccounts` — those are resolved in Task 6.

- [ ] **Step 6: Commit**

```bash
git add internal/store/backend_config.go internal/migrate/files/0033_standalone_mode_collapse.up.sql internal/migrate/files/0033_standalone_mode_collapse.down.sql internal/store/backend_config_test.go
git commit -m "feat(store): collapse standalone_login_mode to enabled/disabled

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Wire the RuntimeHost client into the handler

**Files:**
- Modify: `internal/abs/handler.go` (Handler struct, Deps, NewHandler) and `cmd/silo-plugin-audiobooks/main.go`
- Test: build only

- [ ] **Step 1: Define the validator interface**

In `internal/abs/handler.go`, replace the `HostLoginValidator` interface (`handler.go:64-68`) with:

```go
// ProfileCredentialValidator resolves a third-party "user#profile" /
// "password#pin" login against the Silo host. Implemented by the
// SDK runtimehost client; an interface so tests can stub it.
type ProfileCredentialValidator interface {
	ValidateProfileCredential(ctx context.Context, username, password string) (*runtimehost.ProfileCredential, error)
}
```

Add the import `runtimehost "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"`.

- [ ] **Step 2: Swap the Handler field and Deps**

In the `Handler` struct, replace `hostLogin HostLoginValidator` with `credValidator ProfileCredentialValidator`. In `Deps`, replace the `HostLogin HostLoginValidator` field with `CredValidator ProfileCredentialValidator`. In `NewHandler`, replace `hostLogin: d.HostLogin` with `credValidator: d.CredValidator`.

- [ ] **Step 3: Wire it in main.go**

In `cmd/silo-plugin-audiobooks/main.go`, delete the `hostLoginClient := hostlogin.New(hostBase)` line (`main.go:90`) and its comment. In the `abs.NewHandler(abs.Deps{...})` block (`main.go:213-232`), replace `HostLogin: hostLoginClient,` with:

```go
			CredValidator: runtimehost.NewClient(sdkruntime.Host()),
```

Add the `runtimehost` import. Confirm the constructor name against the SDK: run `grep -rn 'func New' /opt/silo_plugins/continuum-plugin-sdk/pkg/pluginsdk/runtimehost/client.go` and use whatever constructor returns a `*runtimehost.Client` from the `sdkruntime.Host()` handle. Remove the now-unused `hostlogin` import.

- [ ] **Step 4: Verify build fails only where expected**

Run: `go build ./... 2>&1 | head`
Expected: errors only in `handler.go`'s `handleStandaloneLogin` (still references the old `h.hostLogin`) — resolved in Task 6.

- [ ] **Step 5: Commit after Task 6** (this task does not build standalone; commit together with Task 6).

---

### Task 6: Rewrite the two login paths

**Files:**
- Modify: `internal/abs/handler.go` — `handleLogin`, `handleStandaloneLogin`, `completeLogin`
- Delete: `internal/hostlogin/` (entire package)
- Test: `internal/abs/handler_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/abs/handler_test.go` a stub validator and two tests:

```go
type stubValidator struct {
	cred *runtimehost.ProfileCredential
	err  error
}

func (s stubValidator) ValidateProfileCredential(_ context.Context, _, _ string) (*runtimehost.ProfileCredential, error) {
	return s.cred, s.err
}

func TestStandaloneLogin_Success(t *testing.T) {
	h := newTestHandler(t) // existing handler-test harness
	h.credValidator = stubValidator{cred: &runtimehost.ProfileCredential{UserID: "42", ProfileID: "p1"}}
	// standalone_login_mode must be "enabled" in the test backend_config.
	body := strings.NewReader(`{"username":"alice#kids","password":"pw"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStandaloneLogin_BadCreds(t *testing.T) {
	h := newTestHandler(t)
	h.credValidator = stubValidator{err: status.Error(codes.Unauthenticated, "invalid credentials")}
	body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
```

Add imports `google.golang.org/grpc/codes` and `google.golang.org/grpc/status`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/abs/ -run TestStandaloneLogin -v`
Expected: FAIL / compile error.

- [ ] **Step 3: Rewrite `handleLogin`**

```go
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Path A — host-proxied: the host stamped identity headers.
	if userID := r.Header.Get("X-Silo-User-Id"); userID != "" {
		profileID := r.Header.Get("X-Silo-Profile-Id") // empty = primary
		name := r.Header.Get("X-Silo-Profile-Name")
		if name == "" {
			name = r.Header.Get("X-Silo-User-Name")
		}
		h.completeLogin(w, r, userID, profileID, name)
		return
	}
	// Path B — standalone port: validate body credentials via the host RPC.
	h.handleStandaloneLogin(w, r)
}
```

- [ ] **Step 4: Rewrite `handleStandaloneLogin`**

```go
func (h *Handler) handleStandaloneLogin(w http.ResponseWriter, r *http.Request) {
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if store.NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode) == store.StandaloneLoginModeDisabled {
		http.Error(w, "standalone login is disabled on this server", http.StatusUnauthorized)
		return
	}
	if h.credValidator == nil {
		h.logger.Warn("abs.standalone_login: no credential validator configured")
		http.Error(w, "standalone login is unavailable in this deployment", http.StatusServiceUnavailable)
		return
	}
	ip := clientIP(r)
	if !h.loginLimiter.allow(ip) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts; try again shortly", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		http.Error(w, "username and password are required", http.StatusUnauthorized)
		return
	}
	// The plugin never parses '#': the host owns username#profile /
	// password#pin parsing inside ValidateProfileCredential.
	cred, err := h.credValidator.ValidateProfileCredential(r.Context(), body.Username, body.Password)
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated:
			h.logger.Warn("abs.standalone_login: invalid credentials", "ip", ip, "username", body.Username)
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
		default:
			h.logger.Warn("abs.standalone_login: validator error", "ip", ip, "err", err.Error())
			http.Error(w, "login service unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	// Display name: the profile portion of what the user typed (after '#'),
	// else the whole username.
	displayName := body.Username
	if i := strings.LastIndexByte(displayName, '#'); i >= 0 && i < len(displayName)-1 {
		displayName = displayName[i+1:]
	}
	h.completeLogin(w, r, cred.UserID, cred.ProfileID, displayName)
}
```

Add imports `codes`, `status` to `handler.go` if not present.

- [ ] **Step 5: Update `completeLogin` signature**

Change the signature to `func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, userID, profileID, displayName string)`. (Phase 1 made it `(userID, displayName)`; Phase 2 inserts `profileID`.) Inside: pass `profileID` to `IssueAccessToken`/`IssueRefreshToken` (both now take it — Task 2), store it on the `abs_tokens` rows (see Task 14), and pass it to `absUserObject` (Task 16). Update the `handleAuthorize` call path too — `handleAuthorize` reads `a.ProfileID` from `ctxAuth` and includes it where the user object is built.

- [ ] **Step 6: Delete the hostlogin package**

```bash
git rm -r internal/hostlogin
```

Remove every remaining `hostlogin` import across the repo (`grep -rl hostlogin internal cmd`).

- [ ] **Step 7: Run tests + build**

Run: `go build ./... && go test ./internal/abs/ -run 'TestStandaloneLogin|TestHandleLogin' -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(abs): resolve login via ValidateProfileCredential RPC

Replaces the hostlogin HTTP client with the RuntimeHost RPC; login
now resolves a (userID, profileID) pair on both paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Drop the opt-in table and endpoints

**Files:**
- Delete: `internal/server/abs_standalone.go`, `internal/store/abs_standalone_opt_in.go`
- Modify: the route registration that calls `mountABSStandaloneRoutes`
- Create: `internal/migrate/files/0034_drop_opt_ins.up.sql` / `.down.sql`

- [ ] **Step 1: Write the drop migration**

Create `internal/migrate/files/0034_drop_opt_ins.up.sql`:

```sql
DROP TABLE IF EXISTS abs_standalone_opt_ins;
```

Create `internal/migrate/files/0034_drop_opt_ins.down.sql`:

```sql
CREATE TABLE IF NOT EXISTS abs_standalone_opt_ins (
  user_id TEXT PRIMARY KEY
);
```

- [ ] **Step 2: Remove the code**

```bash
git rm internal/server/abs_standalone.go internal/store/abs_standalone_opt_in.go
```

Find the caller of `mountABSStandaloneRoutes` (`grep -rn mountABSStandaloneRoutes internal/server`) and delete that call line.

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: success (compiler confirms no remaining references to the removed methods).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat: drop abs_standalone opt-in table and endpoints

The core profile + PIN model supersedes per-user mobile-login opt-in.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Migration — add `profile_id` to every user-owned table

**Files:**
- Create: `internal/migrate/files/0035_profile_id.up.sql` / `.down.sql`

- [ ] **Step 1: Write the up migration**

Create `internal/migrate/files/0035_profile_id.up.sql`:

```sql
-- Per-profile re-keying. profile_id '' is the canonical primary profile;
-- existing rows backfill to '' so every current user keeps their data
-- under their primary profile.

ALTER TABLE collection         ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE smart_collection   ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE playlist           ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE bookmark           ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE abs_playback_session ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE abs_token          ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';

-- progress: profile_id joins the primary key. Drop and recreate the PK.
ALTER TABLE progress ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE progress DROP CONSTRAINT progress_pkey;
ALTER TABLE progress ADD PRIMARY KEY (user_id, profile_id, book_id);

-- Owner-scoped indexes extended with profile_id.
DROP INDEX IF EXISTS collection_user_pinned_idx;
CREATE INDEX collection_user_pinned_idx ON collection (user_id, profile_id, is_pinned DESC, name);
DROP INDEX IF EXISTS smart_collection_user_idx;
CREATE INDEX smart_collection_user_idx ON smart_collection (user_id, profile_id, is_pinned DESC, name);
DROP INDEX IF EXISTS playlist_user_idx;
CREATE INDEX playlist_user_idx ON playlist (user_id, profile_id, LOWER(name));
DROP INDEX IF EXISTS bookmark_user_book_idx;
CREATE INDEX bookmark_user_book_idx ON bookmark (user_id, profile_id, book_id, position_seconds);
DROP INDEX IF EXISTS progress_user_updated_idx;
CREATE INDEX progress_user_updated_idx ON progress (user_id, profile_id, updated_at DESC);
DROP INDEX IF EXISTS progress_user_updated_visible_idx;
CREATE INDEX progress_user_updated_visible_idx ON progress (user_id, profile_id, updated_at DESC) WHERE hidden_from_continue = FALSE;
DROP INDEX IF EXISTS abs_session_active_idx;
CREATE INDEX abs_session_active_idx ON abs_playback_session (user_id, profile_id, last_update DESC) WHERE closed_at IS NULL;
```

- [ ] **Step 2: Write the down migration**

Create `internal/migrate/files/0035_profile_id.down.sql`:

```sql
ALTER TABLE progress DROP CONSTRAINT progress_pkey;
ALTER TABLE progress ADD PRIMARY KEY (user_id, book_id);
ALTER TABLE collection           DROP COLUMN IF EXISTS profile_id;
ALTER TABLE smart_collection     DROP COLUMN IF EXISTS profile_id;
ALTER TABLE playlist             DROP COLUMN IF EXISTS profile_id;
ALTER TABLE bookmark             DROP COLUMN IF EXISTS profile_id;
ALTER TABLE abs_playback_session DROP COLUMN IF EXISTS profile_id;
ALTER TABLE abs_token            DROP COLUMN IF EXISTS profile_id;
ALTER TABLE progress             DROP COLUMN IF EXISTS profile_id;
```

- [ ] **Step 3: Commit**

```bash
git add internal/migrate/files/0035_profile_id.up.sql internal/migrate/files/0035_profile_id.down.sql
git commit -m "feat(migrate): add profile_id to all user-owned tables

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Re-key the `collection` store (exemplar — full pattern)

**Files:**
- Modify: `internal/store/collection.go`
- Test: `internal/store/collection_test.go`

This task is the worked exemplar; Tasks 10-14 apply the identical pattern to the other store files. The pattern: every method that takes a user/owner id for scoping also takes a `profileID string`; INSERTs add the `profile_id` column; scoping WHEREs add `AND profile_id = $n`; the `Collection` struct gains `ProfileID string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/collection_test.go`:

```go
func TestCollectionsIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) { if err != nil { t.Fatal(err) } }
	must(st.CreateCollection(ctx, Collection{ID: "c1", UserID: "u1", ProfileID: "", Name: "Primary"}))
	must(st.CreateCollection(ctx, Collection{ID: "c2", UserID: "u1", ProfileID: "kids", Name: "Kids"}))

	primary, err := st.ListUserCollections(ctx, "u1", "")
	must(err)
	if len(primary) != 1 || primary[0].ID != "c1" {
		t.Fatalf("primary profile sees %d collections, want [c1]", len(primary))
	}
	kids, err := st.ListUserCollections(ctx, "u1", "kids")
	must(err)
	if len(kids) != 1 || kids[0].ID != "c2" {
		t.Fatalf("kids profile sees %d collections, want [c2]", len(kids))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestCollectionsIsolatedByProfile -v`
Expected: FAIL — compile error (`Collection.ProfileID` undefined; `ListUserCollections` takes 1 arg).

- [ ] **Step 3: Add `ProfileID` to the struct and re-key the methods**

Add `ProfileID string` to the `Collection` struct. Apply these query changes in `internal/store/collection.go`:

`CreateCollection` — add `profile_id` to the INSERT:
```go
		INSERT INTO collection (id, user_id, profile_id, name, color, is_public, is_pinned, cover_book_id)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, $7, NULLIF($8,''))
```
with args `c.ID, c.UserID, c.ProfileID, c.Name, c.Color, c.IsPublic, c.IsPinned, c.CoverBookID`.

`UpdateCollection(ctx, c, ownerID, profileID string)` — when `ownerID != ""` the appended clause becomes `AND user_id = $7 AND profile_id = $8` with both args.

`DeleteCollection(ctx, id, ownerID, profileID string)` — `DELETE FROM collection WHERE id = $1 AND user_id = $2 AND profile_id = $3`.

`GetCollection` — add `profile_id` to the SELECT column list and scan into `c.ProfileID`.

`ListUserCollections(ctx, userID, profileID string)` — `WHERE user_id = $1 AND profile_id = $2`; add `profile_id` to the SELECT list and scan it.

`ListPublicCollections` — add `profile_id` to the SELECT list and scan it; the `WHERE is_public = true` filter is unchanged (public collections cross profiles by design).

`AddCollectionItem(ctx, collectionID, bookID, ownerID, profileID string)` — the `EXISTS (SELECT 1 FROM collection WHERE id = $1 AND user_id = $3)` clause gains `AND profile_id = $4`.

`RemoveCollectionItem(ctx, collectionID, bookID, ownerID, profileID string)` — the inner `SELECT id FROM collection WHERE id = $1 AND user_id = $3` gains `AND profile_id = $4`.

`ListCollectionItems(ctx, collectionID, viewerID, profileID string)` — the inner `WHERE id = $1 AND (user_id = $2 OR is_public)` becomes `WHERE id = $1 AND ((user_id = $2 AND profile_id = $3) OR is_public)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestCollectionsIsolatedByProfile -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/collection.go internal/store/collection_test.go
git commit -m "feat(store): scope collections by profile_id

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Re-key the `smart_collection` store

**Files:** Modify `internal/store/smart_collection.go`; Test `internal/store/smart_collection_test.go`.

Apply the Task 9 pattern. Add `ProfileID string` to the `SmartCollection` struct. Method changes:
- `UpsertSmartCollection` — add `profile_id` to the INSERT column list (as `$3`, shifting later params) and to `ON CONFLICT (id) DO UPDATE` leave the conflict target as `id` (the ULID is globally unique).
- `GetSmartCollection` — add `profile_id` to the SELECT list, scan it.
- `ListSmartCollections(ctx, userID, profileID string, limit int)` — `WHERE (user_id = $1 AND profile_id = $2) OR is_public = TRUE`; the `ORDER BY (user_id = $1) DESC` becomes `ORDER BY (user_id = $1 AND profile_id = $2) DESC`; `limit` becomes `$3`.
- `DeleteSmartCollection(ctx, id, userID, profileID string)` — `WHERE id = $1 AND user_id = $2 AND profile_id = $3`.

Write `TestSmartCollectionsIsolatedByProfile` mirroring Task 9 Step 1 (two smart collections under `u1` with profile `""` and `"kids"`, assert `ListSmartCollections` filters). TDD order: failing test → run → implement → run → commit `feat(store): scope smart collections by profile_id`.

---

### Task 11: Re-key the `playlist` store

**Files:** Modify `internal/store/playlist.go`; Test `internal/store/playlist_test.go`.

Apply the Task 9 pattern. Add `ProfileID string` to the `Playlist` struct. Method changes:
- `CreatePlaylist` — add `profile_id` to the INSERT.
- `UpdatePlaylist(ctx, p, ownerID, profileID string)` — `WHERE id = $5 AND user_id = $6 AND profile_id = $7`.
- `DeletePlaylist(ctx, id, ownerID, profileID string)` — `WHERE id = $1 AND user_id = $2 AND profile_id = $3`.
- `GetPlaylist` — add `profile_id` to the SELECT, scan it.
- `ListUserPlaylists(ctx, userID, profileID string)` — `WHERE user_id = $1 AND profile_id = $2`.
- `AddPlaylistItem(ctx, playlistID, libraryItemID, episodeID, ownerID, profileID string)` — the owner check `SELECT user_id FROM playlist WHERE id = $1` becomes `SELECT user_id, profile_id FROM playlist WHERE id = $1`; reject unless both `ownerCheck == ownerID` AND `profileCheck == profileID`.
- `RemovePlaylistItem(ctx, ..., ownerID, profileID string)` — the `EXISTS (SELECT 1 FROM playlist WHERE id = $1 AND user_id = $4)` clause gains `AND profile_id = $5`.
- `ListPlaylistItems(ctx, playlistID, viewerID, profileID string)` — the `WHERE pi.playlist_id = $1 AND (p.user_id = $2 OR p.is_public = TRUE)` becomes `AND ((p.user_id = $2 AND p.profile_id = $3) OR p.is_public = TRUE)`.

Write `TestPlaylistsIsolatedByProfile` mirroring Task 9. TDD order as before; commit `feat(store): scope playlists by profile_id`.

---

### Task 12: Re-key the `progress` store

**Files:** Modify `internal/store/progress.go`, `internal/store/streak.go`, `internal/store/reading_goal.go`; Test `internal/store/progress_test.go`.

Apply the Task 9 pattern. Add `ProfileID string` to the `Progress` struct. Method changes:
- `UpdateProgressPosition(ctx, userID, profileID, bookID string, currentSeconds int)` — INSERT adds `profile_id`; `ON CONFLICT (user_id, profile_id, book_id)`.
- `UpsertProgress` — INSERT adds `profile_id` (the struct carries it); `ON CONFLICT (user_id, profile_id, book_id)`.
- `GetProgress(ctx, userID, profileID, bookID string)` — `WHERE user_id = $1 AND profile_id = $2 AND book_id = $3`; add `profile_id` to SELECT + scan.
- `ListInProgress(ctx, userID, profileID string, limit int)` — `WHERE user_id = $1 AND profile_id = $2 AND is_finished = FALSE AND ...`; `limit` becomes `$3`.
- `DeleteProgress(ctx, userID, profileID, bookID string)` — `WHERE user_id = $1 AND profile_id = $2 AND book_id = $3`.
- `ListRecentProgress(ctx, userID, profileID string, limit int)` — `WHERE user_id = $1 AND profile_id = $2`.
- `HideProgressFromContinue` / `UnhideProgressFromContinue` — add `profileID` param; `WHERE user_id = $1 AND profile_id = $2 AND book_id = $3`.
- `streak.go` `StreakForUser(ctx, userID, profileID string, loc)` — `WHERE user_id = $1 AND profile_id = $2`; the timezone arg shifts to `$3`.
- `reading_goal.go` `GoalProgressForUser` books branch — the `progress` COUNT query gains `AND profile_id = $N`; thread `profileID` into `GoalProgressForUser`'s signature.

Extend `TestUpsertProgressPersistsDuration` (from Phase 1) with `ProfileID` and add `TestProgressIsolatedByProfile` (two `progress` rows for `u1`/`b1` under `""` and `"kids"`, assert `GetProgress` returns the right one per profile). TDD order as before; commit `feat(store): scope progress by profile_id`.

---

### Task 13: Re-key the `bookmark` store

**Files:** Modify `internal/store/bookmark.go`; Test `internal/store/bookmark_test.go`.

Apply the Task 9 pattern. Add `ProfileID string` to the `Bookmark` struct. Method changes:
- `InsertBookmark` — add `profile_id` to the INSERT.
- `ListBookmarks(ctx, userID, profileID, bookID string)` — `WHERE user_id = $1 AND profile_id = $2 AND book_id = $3`.
- `DeleteBookmark(ctx, id, userID, profileID string)` — `WHERE id = $1 AND user_id = $2 AND profile_id = $3`.
- `UpsertBookmarkAt` — the lookup `SELECT id FROM bookmark WHERE user_id = $1 AND book_id = $2 AND position_seconds = $3` gains `AND profile_id`; the INSERT adds `profile_id`.
- `DeleteBookmarkAt(ctx, userID, profileID, bookID string, positionSeconds int)` — `WHERE user_id = $1 AND profile_id = $2 AND book_id = $3 AND position_seconds = $4`.

Write `TestBookmarksIsolatedByProfile` mirroring Task 9. TDD order as before; commit `feat(store): scope bookmarks by profile_id`.

---

### Task 14: Re-key the `abs_token` and `abs_playback_session` stores

**Files:** Modify `internal/store/abs_token.go`, `internal/store/abs_session.go`; Test `internal/store/abs_token_test.go`.

`abs_token` — add `ProfileID string` to the `ABSToken` struct:
- `InsertABSToken` — add `profile_id` to the INSERT (the struct carries it; `completeLogin` sets it — Task 6 Step 5).
- `GetABSTokenByJTI` — add `profile_id` to the SELECT, scan into `ABSToken.ProfileID`. (`bearerAuth` reads the profile from the JWT claim, not this row; the column is for audit/listing parity.)
- `ListABSTokens` — add `profile_id` to the SELECT, scan it.
- `RevokeABSToken` / `RevokeABSTokenByJTI` / `TouchABSToken` — unchanged (JTI is globally unique).

`abs_playback_session` — add `ProfileID string` to the `ABSSession` struct:
- `InsertABSSession` — add `profile_id` to the INSERT.
- `GetABSSession` — add `profile_id` to the SELECT, scan it.
- `ListActiveABSSessionsForUser(ctx, userID, profileID string, limit int)` — `WHERE closed_at IS NULL AND user_id = $1 AND profile_id = $2`.
- `UpdateABSSession` / `CloseABSSession` — keyed by `id` (a ULID); unchanged. `ListActiveABSSessions` / `CountActiveABSSessions` / `ReapIdleABSSessions` are admin/global; unchanged.

Write `TestABSSessionsIsolatedByProfile` (two active sessions for `u1` under `""` and `"kids"`, assert `ListActiveABSSessionsForUser` filters). TDD order as before; commit `feat(store): scope abs tokens and sessions by profile_id`.

---

### Task 15: Thread `profileID` through every handler call site

**Files:**
- Modify: `internal/abs/handler.go`, `internal/abs/collections_handler.go`, `internal/abs/smart_collection_handler.go`, `internal/abs/playlists_handler.go`, `internal/abs/bookmarks_handler.go`, `internal/abs/rss_feed_handler.go`, `internal/abs/continue_listening.go`, and any other `internal/abs/*.go` calling a re-keyed store method.

After Tasks 9-14 the re-keyed store methods take a new `profileID` parameter, so the build is broken at every call site. This task fixes them mechanically.

- [ ] **Step 1: List every broken call site**

Run: `go build ./internal/abs/... 2>&1`
Expected: a list of "not enough arguments" errors — one per call site.

- [ ] **Step 2: Fix each call site**

At every handler that has `a, _ := absAuthFrom(r)`, the profile is `a.ProfileID`. For each compile error, pass `a.ProfileID` as the new `profileID` argument in the documented position. Handlers that previously passed `a.UserID` for scoping now pass `a.UserID, a.ProfileID`. For RSS feed handlers (`rss_feed_handler.go`), the `ListCollectionItems` call inside `collectionFeedEpisodes` takes `ownerID` — pass the feed's profile context; since RSS feeds are slug-gated and not yet profile-scoped, pass `""` (primary) for `profileID` there and leave a `// TODO(profiles): RSS feeds are not profile-scoped` comment.

- [ ] **Step 3: Verify the build is clean**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Run the abs package tests**

Run: `go test ./internal/abs/...`
Expected: PASS (update any test that calls a re-keyed store method to pass a `profileID`).

- [ ] **Step 5: Commit**

```bash
git add internal/abs/
git commit -m "feat(abs): thread profile_id through all handler store calls

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 16: `absUserObject` and `handleMe`/`handleAuthorize` use the profile

**Files:**
- Modify: `internal/abs/handler.go` — `absUserObject` (added in Phase 1 Task 8), `handleMe`, `handleAuthorize`
- Test: `internal/abs/handler_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestAbsUserObjectScopesProgressToProfile(t *testing.T) {
	h := newTestHandler(t)
	ctx := context.Background()
	_ = h.store.UpsertProgress(ctx, store.Progress{UserID: "u1", ProfileID: "", BookID: "b1", CurrentSeconds: 10})
	_ = h.store.UpsertProgress(ctx, store.Progress{UserID: "u1", ProfileID: "kids", BookID: "b2", CurrentSeconds: 20})

	obj := h.absUserObject(ctx, "u1", "kids", "Kid", "lib1")
	prog := obj["mediaProgress"].([]map[string]any)
	if len(prog) != 1 || prog[0]["libraryItemId"] != "b2" {
		t.Fatalf("mediaProgress = %v, want only b2", prog)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/abs/ -run TestAbsUserObjectScopesProgressToProfile -v`
Expected: FAIL — compile error (`absUserObject` takes 4 args).

- [ ] **Step 3: Add `profileID` to `absUserObject`**

Change the signature to `absUserObject(ctx, userID, profileID, displayName, defaultLibraryID string)` and update its `ListRecentProgress` call to `h.store.ListRecentProgress(ctx, userID, profileID, 200)`.

- [ ] **Step 4: Update the three callers**

`completeLogin` passes `profileID` (its own param). `handleAuthorize` passes `a.ProfileID`. `handleMe` passes `a.ProfileID`.

- [ ] **Step 5: Run test + build**

Run: `go build ./... && go test ./internal/abs/ -run 'TestAbsUserObject|TestHandleMe|TestHandleAuthorize' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/abs/handler.go internal/abs/handler_test.go
git commit -m "feat(abs): scope the user object's mediaProgress to the active profile

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 17: Send `X-Profile-Id` from the web SPA

**Files:**
- Create: `web/src/lib/profile.ts`
- Modify: `web/src/api/client.ts:52-55` (`authHeaders`) and `client.ts:130-137` (the 401-retry branch)
- Test: `web/src/api/client.test.ts` (create if absent)

- [ ] **Step 1: Add a profile-id capture helper**

Create `web/src/lib/profile.ts`, modelled on `web/src/lib/identity.ts`'s `captureRoleFromURL` / `currentRole` pattern:

```ts
// Active profile id, captured from the ?profileId= query param the core
// app puts on the plugin SPA URL, cached in sessionStorage. Empty string
// means the primary profile.
const KEY = 'silo.profileId';

export function captureProfileFromURL(): void {
  const v = new URLSearchParams(window.location.search).get('profileId');
  if (v !== null) sessionStorage.setItem(KEY, v);
}

export function currentProfileId(): string {
  return sessionStorage.getItem(KEY) ?? '';
}
```

Call `captureProfileFromURL()` wherever `captureFromURL()` / `captureRoleFromURL()` are called at SPA bootstrap (search `web/src` for `captureRoleFromURL`).

- [ ] **Step 2: Write the failing test**

Create `web/src/api/client.test.ts`:

```ts
import { describe, it, expect, beforeEach } from 'vitest';
import { authHeaders } from './client';

describe('authHeaders', () => {
  beforeEach(() => sessionStorage.clear());
  it('includes X-Profile-Id when a profile is active', () => {
    sessionStorage.setItem('silo.profileId', 'kids');
    expect(authHeaders()['X-Profile-Id']).toBe('kids');
  });
  it('omits X-Profile-Id for the primary profile', () => {
    expect(authHeaders()['X-Profile-Id']).toBeUndefined();
  });
});
```

Export `authHeaders` from `client.ts` if it is not already exported.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd web && npx vitest run src/api/client.test.ts`
Expected: FAIL — `X-Profile-Id` undefined.

- [ ] **Step 4: Add the header in `authHeaders`**

Replace `authHeaders` (`web/src/api/client.ts:52-55`):

```ts
export function authHeaders(): Record<string, string> {
  const h: Record<string, string> = {};
  const t = getCachedToken();
  if (t) h.Authorization = `Bearer ${t}`;
  const p = currentProfileId();
  if (p) h['X-Profile-Id'] = p;
  return h;
}
```

Add `import { currentProfileId } from '@/lib/profile';` (match the existing import style in the file).

- [ ] **Step 5: Cover the 401-retry branch**

In `authedFetch`, the retry branch (`client.ts:130-137`) rebuilds headers from `init.headers` + `Authorization` only. Add the profile header there too:

```ts
  return fetch(input, {
    ...init,
    headers: {
      ...(init?.headers as Record<string, string> | undefined),
      Authorization: `Bearer ${freshToken}`,
      ...(currentProfileId() ? { 'X-Profile-Id': currentProfileId() } : {}),
    },
    credentials: init?.credentials ?? 'include',
  });
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd web && npx vitest run src/api/client.test.ts`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add web/src/lib/profile.ts web/src/api/client.ts web/src/api/client.test.ts
git commit -m "feat(web): send X-Profile-Id so the SPA scopes to the active profile

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 18: Full verification and deploy

- [ ] **Step 1: Go suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: Web build + tests**

Run: `cd web && npx vitest run && pnpm run build`
Expected: PASS / clean build.

- [ ] **Step 3: Bump the manifest**

Set `cmd/silo-plugin-audiobooks/manifest.json` `"version"` to `1.2.0`. Commit:

```bash
git add cmd/silo-plugin-audiobooks/manifest.json
git commit -m "chore: bump plugin to 1.2.0 for profile-aware auth

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Deploy**

Run: `/opt/silo_plugins/install-plugin.sh /opt/silo_plugins/silo-plugin-audiobooks`
Expected: ends with `→ plugin live (HTTP 200)`.

- [ ] **Step 5: Smoke-test profile login**

With a real test account `alice` that has a `kids` profile, against the published port:

```bash
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"username":"alice#kids","password":"<alice-password>"}' \
  http://localhost:9998/login | head -c 400
```

Expected: HTTP 200, a `user` object, and a decodable access token whose `pid` claim is the `kids` profile id. A bare `alice` login yields a token with empty `pid`. Confirm the two logins see different collections/progress.

---

## Self-Review

- **Spec coverage:** §2.1 login paths → Tasks 5, 6; §2.2 `standalone_login_mode` collapse → Tasks 4, 7; §2.3 `profile_id` JWT claim → Tasks 2, 3; §2.4 re-keying migration + store → Tasks 8-15; §2.5 web SPA → Task 17; §2.6 display name → Task 6 Step 4; §2.7 migration safety → Task 8 (`DEFAULT ''` backfill). Prerequisite check → Task 1. All Phase 2 spec items are covered.
- **Type consistency:** `completeLogin(w, r, userID, profileID, displayName)` — five params, defined in Task 6, called consistently. `absUserObject(ctx, userID, profileID, displayName, defaultLibraryID)` — Task 16. `IssueAccessToken`/`IssueRefreshToken` gain `profileID` — Task 2, consumed in Task 6. Store methods gain `profileID` consistently — Tasks 9-14 — and Task 15 fixes all call sites in one mechanical pass driven by the compiler.
- **Ordering:** JWT/ctx (2,3) → mode collapse (4) → validator wiring + login (5,6) → opt-in removal (7) → migration (8) → store re-keying (9-14) → call-site fixup (15) → user-object (16) → SPA (17). Each store task (9-14) is independently testable; Task 15 is the single point where the full build goes green again.
- **Placeholder scan:** Tasks 10-14 give each file's exact method list and the specific SQL transformation rather than repeating the full exemplar — this is per-file real code, not "similar to Task 9". The one explicit deferral (RSS feeds not profile-scoped, Task 15 Step 2) is marked with a `TODO(profiles)` and is intentional: RSS feeds are slug-gated public artifacts, out of the per-profile scope per spec §2.
- **Known assumption:** `newTestHandler`/`newStore` test helpers are referenced as the package's existing harness; if a differently-named helper exists, use it. Task 5 Step 3 verifies the SDK constructor name against the actual SDK source before use.
