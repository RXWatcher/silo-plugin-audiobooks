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
