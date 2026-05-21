# Feature inventory — audiobooks + ebooks plugins

Date: 2026-05-21
Status: post-triage. Items the operator cut have been moved to the
"Removed from scope" section at the bottom; everything else is
either shipped or actively planned.

---

## Kept — Audiobooks plugin

### ABS API compatibility (shipped)

- Dual-mount routes (root + `/api/*` + `/abs/api/*`) so the official
  ABS apps connect without path tweaks.
- Full login envelope (`permissions`, `librariesAccessible`,
  `mediaProgress`, `bookmarks`, `serverSettings`, `ereaderDevices`).
- `x-refresh-token` header convention + `/api/authorize` re-auth.
- ServerVersion 2.26 (unlocks the mobile app's JWT path).
- Socket `init` / `auth_failed` events + `{data}` wrapper on
  `user_item_progress_updated`.
- Library detail wrapper + personalized shelf entity shapes.
- Podcast personalized shelves (recent-episodes, newest-podcasts,
  listen-again).
- Bookmarks CRUD on `/me/item/{id}/bookmark`.
- Download endpoint `/api/items/{id}/file/{ino}` with Range + per-ext
  MIME.
- `/me/items-in-progress` + Continue-Listening management.
- Library multi-bucket search.
- Plural Socket.io events (`items_added`, `library_updated`,
  `episode_download_finished`).
- Collections CRUD at upstream paths.
- Playlists CRUD with episode-scoped entries.
- Custom metadata providers (admin CRUD + proxied search).
- RSS-feed-publish — item / series / collection renderings.
- Share links with public audio bytes.

### Features (shipped)

- Sleep-timer with 30-second fade.
- Smart Collections — rule DSL, evaluator, CRUD.
- Embedding-based similar-items (pgvector + HNSW; OpenAI / Gemini /
  Ollama).
- Reading streak counter.
- Reading-session telemetry + heatmap + year-in-review.
- Reading goals (books + hours).
- Per-book activity timeline.
- Notification preferences.
- Content restrictions / family mode.

### Frontend (shipped)

- Command palette (Cmd-K).
- Keyboard shortcut help (?).
- Atmosphere mode overlay.

---

## Kept — Ebooks plugin

### Backend (shipped)

- Smart Collections (DSL + evaluator + CRUD).
- Embedding-based similar-items.
- `foliate-js` vendored locally (no more sibling-clone dep).
- Content restrictions / family mode.
- Custom metadata providers.
- Send-to-ereader (device registry + SMTP send).
- Readwise.io export.
- Hardcover.app sync.
- Metadata enrichment (OpenLibrary + Google Books).
- Dictionary lookup (Wiktionary).
- In-text translation (LibreTranslate-compatible).
- Custom font upload + serve.
- Reading streak counter.
- Reading goals (books).
- Year-in-review stats.
- Per-book activity timeline.
- Notification preferences.
- Share links (no audio — ebook download via the existing file
  routes).
- Scheduled cleanup tasks.

### Frontend (shipped)

- Command palette (Cmd-K).
- Keyboard shortcut help (?).
- Atmosphere overlay component (built; not mounted in reader yet).
- Screen wake-lock hook (wired into reader).
- E-ink mode hook (CSS rules in place).
- TTS controller hook with MediaSession (built; not yet mounted as a
  reader button).

---

## Removed from scope

These items were cut during triage. They are NOT on the planned
roadmap.

Code-already-shipped items in this list (BookDrop, audit log, sync,
exports, etc.) currently still live in `main`. A follow-up call
decides whether to leave them dormant or revert the commits. The
operator should make that decision explicitly — none of the cut
features are getting further investment.

### Cut from shipped (audiobooks)

- BookDrop watched folder (admin review queue + embedded ID3 cover).
- Bookmark UI auto-titles + inline rename.
- Personal data export → ZIP.
- Restore from export.
- Per-library settings (JSONB).
- Audit log.
- Web Push subscriptions.
- HLC + bookmark change-log + sync push/pull.
- Field-level LWW merge helper.

### Cut from shipped (ebooks)

- Markdown annotation export.
- Annotation Notebook (cross-book aggregated view).
- Shareable quote-image generator.
- Personal data export + restore.
- Per-library settings.
- Audit log.
- HLC + annotation change-log + sync push/pull.
- Field-level LWW merge helper.
- Markdown export button in reader.
- Quote-image generator (built; needs reader-side wiring).

### Cut from "planned but not done"

- Document-to-EPUB conversion on import.
- Annotations + user_data sections in ebook restore.
- VAPID-encrypted Web Push send pipeline.
- Backend BookDrop ingest handler.
- Per-section audit instrumentation across admin handlers.
- Web Push ebook port.
- Magnifier loupe (ebook reader touch).
- KOSync conflict-resolver UI.
- AI RAG sidebar (per-book chat).
- The remaining SPA pages backlog as a category.

---

## Decision needed

The cut list above includes a substantial amount of code that has
already shipped to `origin/main`. Three options for each cut
feature:

1. **Revert the commits** — clean removal, gets the code out of the
   tree, runs migrations down. Highest cleanup; loses the work.
2. **Leave dormant** — code stays, no further investment, no UI
   surfaces it, scheduled tasks for cut features get disabled in
   the manifest. Lowest risk; carries dead weight.
3. **Keep but un-cut a subset** — if anything on the cut list was
   removed in haste, name it and we put it back on the kept list
   without reverting.

Tell me which path per cut feature (or apply one of the three
across the whole list).
