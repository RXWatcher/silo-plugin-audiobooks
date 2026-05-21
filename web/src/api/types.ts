// Types mirror the Go backend's API shapes (internal/server/*.go and the
// audiobook_backend.v1 contract from internal/backend/types.go).

export interface AudiobookSummary {
  id: string;
  title: string;
  authors?: string[];
  narrators?: string[];
  cover_url?: string;
  duration_seconds?: number;
  year?: number;
  rating?: number;
  library_id?: number;
  library_name?: string;
  media_type?: string;
}

export interface AudiobookFile {
  index: number;
  mime_type: string;
  format: string;
  duration_seconds: number;
  size_bytes?: number;
  /**
   * Portal-signed URL the browser puts in <audio src>. Includes a short-TTL
   * signed media token so the backend can authenticate without the browser
   * sending Authorization headers.
   */
  stream_url?: string;
}

export interface AudiobookChapter {
  title: string;
  start_seconds: number;
  end_seconds: number;
}

export interface AudiobookDetail {
  id: string;
  title: string;
  authors?: string[];
  narrators?: string[];
  cover_url?: string;
  duration_seconds: number;
  year?: number;
  isbn?: string;
  publisher?: string;
  series?: string;
  series_position?: string;
  description?: string;
  genres?: string[];
  files: AudiobookFile[];
  chapters?: AudiobookChapter[];
}

export interface PageEnvelope<T> {
  items: T[];
  next_cursor?: string;
  total?: number;
}

export interface Progress {
  user_id: string;
  book_id: string;
  current_seconds: number;
  progress_pct: number;
  is_finished: boolean;
  updated_at: string;
}

export interface Bookmark {
  id: string;
  user_id: string;
  book_id: string;
  position_seconds: number;
  chapter_id?: string;
  note: string;
  created_at: string;
}

export interface Rating {
  user_id: string;
  book_id: string;
  rating: number;
}

export interface UserRequest {
  id: string;
  user_id: string;
  title: string;
  author?: string;
  isbn?: string;
  status: string;
  target_plugin_id: string;
  external_id?: string;
  denied_reason?: string;
  failure_reason?: string;
  created_at: string;
  updated_at: string;
  fulfilled_at?: string;
}

export interface Collection {
  id: string;
  user_id: string;
  name: string;
  color?: string;
  is_public: boolean;
  is_pinned: boolean;
  cover_book_id?: string;
  created_at: string;
}

export interface CollectionItem {
  collection_id: string;
  book_id: string;
  position: number;
  added_at: string;
}

// Smart Collections — rule-based dynamic collections. The
// queryDef mirrors the host's QueryDefinition shape with an
// audiobook field/sort catalog (see internal/smartcoll/query.go).
// Server normalises + validates; the SPA edits the same shape.
export interface SmartCollectionRule {
  field: string;
  op: string;
  value: unknown;
}

export interface SmartCollectionGroup {
  match: 'all' | 'any';
  rules: SmartCollectionRule[];
}

export interface SmartCollectionSort {
  field: string;
  order: 'asc' | 'desc';
}

export interface SmartCollectionQuery {
  library_ids?: number[];
  match: 'all' | 'any';
  groups: SmartCollectionGroup[];
  sort: SmartCollectionSort;
  limit?: number;
}

// Per-book activity event. Server merges progress / bookmark /
// session / rating / share-link rows for one (user, book).
export interface ActivityEvent {
  at: string;
  kind:
    | 'progress'
    | 'bookmark'
    | 'session_opened'
    | 'session_closed'
    | 'rated'
    | 'shared'
    | string;
  payload?: Record<string, unknown>;
}

// Share links — slug-based public capability minted by the owner
// of an item. Expires_at can be null for "no expiry".
export interface ShareLink {
  id: string;
  user_id: string;
  slug: string;
  item_id: string;
  expires_at: string | null;
  max_uses: number;
  use_count: number;
  created_at: string;
}

// Content restrictions — admin sets per-user allow/deny rules.
export interface ContentRestriction {
  user_id: string;
  library_ids?: number[];
  blocked_genres?: string[];
  blocked_tags?: string[];
  blocked_authors?: string[];
  blocked_narrators?: string[];
  block_explicit?: boolean;
  created_at?: string;
  updated_at?: string;
}

// Custom metadata provider — admin-registered external HTTP search
// endpoint that follows the upstream custom-metadata-provider spec.
export interface CustomMetadataProvider {
  id: string;
  name: string;
  url: string;
  auth_header?: string;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

// Notification preferences — per (category, delivery) toggle.
// Missing rows default to enabled (opt-out semantics).
export interface NotificationPref {
  user_id: string;
  category: string;
  delivery: 'inapp' | 'email' | 'push' | string;
  enabled: boolean;
  updated_at?: string;
}

// Reading goals — per (year, kind) target. kind: "books" | "hours".
export interface ReadingGoal {
  user_id: string;
  year: number;
  kind: 'books' | 'hours' | string;
  target: number;
  created_at?: string;
  updated_at?: string;
}

export interface GoalProgress {
  year: number;
  kind: string;
  target: number;
  actual: number;
  percent_complete: number;
  on_pace_for_target: boolean;
  days_into_year: number;
  days_in_year: number;
}

// Heatmap response — daily session counts + listening hours.
export interface HeatmapDay {
  date: string;
  sessions: number;
  seconds: number;
}

export interface HeatmapResponse {
  days: HeatmapDay[];
}

// Year-in-review aggregate.
export interface YearTopBook {
  book_id: string;
  title?: string;
  authors?: string[];
  seconds_listened: number;
  is_finished?: boolean;
}

export interface YearStats {
  year: number;
  total_hours: number;
  books_finished: number;
  distinct_days: number;
  top_books: YearTopBook[];
  top_authors?: { name: string; seconds: number }[];
  top_narrators?: { name: string; seconds: number }[];
  longest_session_seconds?: number;
}

export interface SmartCollection {
  id: string;
  userId: string;
  name: string;
  description?: string;
  color?: string;
  isPublic: boolean;
  isPinned: boolean;
  queryDef: SmartCollectionQuery;
  createdAt: number;
  updatedAt: number;
}

export interface PathRemap {
  source_path: string;
  target_path: string;
}

export interface LibraryInfo {
  id: number;
  name: string;
  media_type?: string;
  backend_plugin_id?: string;
  backend_library_id?: number;
  enabled?: boolean;
  sort_order?: number;
}

export interface BackendConfig {
  target_backend_plugin_id: string;
  target_backend_installation_id?: string;
  target_request_provider_plugin_id: string;
  target_request_provider_installation_id?: string;
  auto_approve_requests: boolean;
  streaming_mode: 'proxy' | 'cache' | 'direct';
  cache_dir: string;
  cache_max_size_gb: number;
  cache_download_concurrency: number;
  path_remappings: PathRemap[];
  abs_access_token_ttl_hours: number;
  abs_refresh_token_ttl_days: number;
  standalone_http_listen: string;
  standalone_login_mode: StandaloneLoginMode;
  libraries?: LibraryInfo[];
}

// StandaloneLoginMode controls whether the standalone-port /abs/api/login
// accepts username+password from Audiobookshelf clients, and whether each
// account must opt in first. Mirrors the Go enum in
// internal/store/backend_config.go.
export type StandaloneLoginMode = 'disabled' | 'opt_in' | 'all_accounts';

export const STANDALONE_LOGIN_MODES: StandaloneLoginMode[] = [
  'disabled',
  'opt_in',
  'all_accounts',
];

export interface ABSStandaloneOptInState {
  mode: StandaloneLoginMode;
  enabled: boolean;
}

// Podcasts — server returns the bare store row shape (snake_case via the
// Go json tags' default rendering). Time fields arrive as RFC3339 strings.
export interface Podcast {
  id: string;
  library_id: number;
  title: string;
  author?: string;
  description?: string;
  cover_url?: string;
  language?: string;
  explicit: boolean;
  itunes_category?: string;
  feed_url?: string;
  last_refreshed_at?: string | null;
  refresh_interval_minutes: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface PodcastEpisode {
  id: string;
  podcast_id: string;
  guid: string;
  title: string;
  description?: string;
  audio_url: string;
  audio_mime_type?: string;
  audio_bytes?: number;
  duration_seconds: number;
  episode_index?: number | null;
  season_index?: number | null;
  published_at?: string | null;
  cover_url?: string;
  created_at: string;
  updated_at: string;
}

export interface PodcastEpisodeProgress {
  user_id: string;
  episode_id: string;
  current_seconds: number;
  progress_pct: number;
  is_finished: boolean;
  updated_at: string;
}

export interface ABSSession {
  id: string;
  user_id: string;
  book_id: string;
  device_id: string;
  device_info?: Record<string, unknown>;
  play_method: string;
  media_player?: string;
  start_time: number;
  current_time: number;
  started_at: string;
  last_update: string;
  closed_at?: string;
}

export interface ABSToken {
  id: string;
  user_id: string;
  jti: string;
  device_id?: string;
  device_name?: string;
  device_info?: Record<string, unknown>;
  last_used_at: string;
  expires_at: string;
  revoked_at?: string;
  created_at: string;
}

export interface ListeningStats {
  user_id: string;
  book_id: string;
  listened_seconds: number;
  last_position: number;
  updated_at?: string;
}

export interface PlaybackSession {
  id: string;
  book_id: string;
  current_seconds: number;
}

export interface AuthorSummary {
  id: string;
  name: string;
  book_count?: number;
}

export interface SeriesSummary {
  id: string;
  name: string;
  book_count?: number;
}

export interface NarratorSummary {
  id: string;
  name: string;
  book_count?: number;
}

export interface InstalledCapability {
  type: string;
  id?: string;
  display_name?: string;
  description?: string;
  metadata?: Record<string, unknown>;
}

export interface InstalledBackend {
  id: number;
  plugin_id: string;
  display_name: string;
  enabled: boolean;
  capabilities: InstalledCapability[];
  audiobook_backend?: InstalledCapability;
  audiobook_roles: string[];
  summary?: string;
}
