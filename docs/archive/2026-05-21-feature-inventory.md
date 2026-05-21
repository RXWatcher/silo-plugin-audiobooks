# Feature inventory — audiobooks + ebooks plugins

Date: 2026-05-21
Status: final. Every kept backend feature has UI. Nothing is "still
backend-only."

---

## Audiobooks plugin

### ABS API compatibility

- Dual-mount routes (root + `/api/*` + `/abs/api/*`)
- Full login envelope, refresh + reauth, ServerVersion 2.26
- Socket `init` / `auth_failed`, `{data}` wrapper on
  `user_item_progress_updated`, plural events (`items_added`,
  `library_updated`, `episode_download_finished`)
- Library detail wrapper + personalized shelf shapes
- Podcast personalized shelves
- Bookmarks CRUD; downloads with Range; items-in-progress
- Multi-bucket library search
- Collections CRUD at upstream paths
- Playlists CRUD with episode-scoped entries
- Custom metadata providers
- RSS-feed-publish — item / series / collection
- Share links with public audio bytes

### Features

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
- Smart Collections — list + builder + live preview
- Stats dashboard — streak, goals, heatmap, year-in-review
- Settings page — display preferences (atmosphere toggle),
  share-links manager, notification preferences toggle matrix
- Per-book activity timeline on book detail
- Admin: Libraries / Requests / Providers / **Metadata sources**
  / **Restrictions** / Sessions / Tokens

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
- Reader: screen wake-lock, e-ink mode CSS, TTS controls (built
  into reader directly), custom font picker (upload + select
  from dropdown), Dictionary + Translate selection-menu actions
- Smart Collections — list + builder + live preview
- Stats dashboard — streak, goals, year-in-review
- Settings page — display preferences (atmosphere + e-ink
  toggles), e-reader devices, Readwise integration, Hardcover
  integration, share-links manager, notification preferences
- Per-book activity timeline on book detail
- Admin: Libraries / Requests / Providers / **Metadata sources**
  / **Restrictions** / Cache / Reader integrations / Delivery /
  Settings
