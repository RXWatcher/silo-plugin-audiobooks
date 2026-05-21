# Feature inventory — audiobooks + ebooks plugins

Date: 2026-05-21
Status: post-triage + SPA catch-up in progress.

Backend reflects the post-triage state (cuts deleted, kept features
still on main). SPA section now lists what has UI shipped and what
is still backend-only.

---

## Audiobooks plugin

### ABS API compatibility

- Dual-mount routes (root + `/api/*` + `/abs/api/*`)
- Full login envelope, refresh + reauth, ServerVersion 2.26
- Socket `init` / `auth_failed`, `{data}` wrapper on
  `user_item_progress_updated`, plural events
  (`items_added`, `library_updated`, `episode_download_finished`)
- Library detail wrapper + personalized shelf shapes
- Podcast personalized shelves
- Bookmarks CRUD; downloads with Range; items-in-progress
- Multi-bucket library search
- Collections CRUD at upstream paths
- Playlists CRUD with episode-scoped entries
- Custom metadata providers
- RSS-feed-publish — item / series / collection
- Share links with public audio bytes

### Features (backend)

- Sleep-timer with 30-second fade
- Smart Collections — DSL + evaluator + CRUD
- Embedding similar-items (pgvector + HNSW)
- Reading streak counter
- Reading-session telemetry + heatmap + year-in-review
- Reading goals (books + hours)
- Per-book activity timeline
- Notification preferences
- Content restrictions / family mode

### Frontend (SPA)

- Command palette (Cmd-K)
- Keyboard shortcut help (?)
- Atmosphere mode overlay (mounted in Layout)
- **Smart Collections** — list + builder + live preview
- **Stats dashboard** — streak, goals, heatmap, year-in-review
- **Settings page** — display preferences (atmosphere toggle),
  share-links manager, notification preferences toggle matrix
- **Per-book activity timeline** — mounted on detail page

### Still backend-only

- Content restrictions (admin UI missing)
- Custom metadata providers (admin UI missing)
- Custom collections CRUD (existing surface, may want richer UI)

---

## Ebooks plugin

### Backend

- Smart Collections (DSL + evaluator + CRUD)
- Embedding similar-items
- `foliate-js` vendored locally
- Content restrictions / family mode
- Custom metadata providers
- Send-to-ereader (device registry + SMTP send)
- Readwise.io export
- Hardcover.app sync
- Metadata enrichment (OpenLibrary + Google Books)
- Dictionary lookup (Wiktionary)
- In-text translation (LibreTranslate)
- Custom font upload + serve
- Reading streak counter
- Reading goals (books)
- Year-in-review stats
- Per-book activity timeline
- Notification preferences
- Share links
- Scheduled cleanup tasks

### Frontend (SPA)

- Command palette (Cmd-K)
- Keyboard shortcut help (?)
- Reader: screen wake-lock hook, e-ink mode CSS class
- TTS controller hook + MediaSession (built, not yet wired to a
  reader button)
- **Smart Collections** — list + builder + live preview
- **Stats dashboard** — streak, goals, year-in-review
- **Settings page** — display preferences (atmosphere + e-ink
  toggles), e-reader devices, Readwise integration, Hardcover
  integration, share-links manager, notification preferences
- **Per-book activity timeline** — mounted on detail page

### Still backend-only

- Custom font picker (uploaded fonts not yet selectable in reader)
- TTS "Read aloud" button (hook exists; reader button missing)
- Dictionary popover (server route exists; reader selection-menu
  action missing)
- Translation popover (same)
- Content restrictions (admin UI missing)
- Custom metadata providers (admin UI missing)
