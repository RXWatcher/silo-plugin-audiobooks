import type { AudiobookFile } from '@/api/types';

export type PlaybackSourceMode = 'stream' | 'download';

export type PlaybackSource = {
  mode: PlaybackSourceMode;
  label: string;
  /**
   * Look up the playback URL for a given file. The portal embeds a signed,
   * short-TTL media token in each file's stream_url, so the browser puts it
   * straight into <audio src> without needing to authenticate to the host
   * plugin proxy with an Authorization header (which it can't send on
   * tag-issued requests).
   */
  urlForFile: (file: AudiobookFile) => string;
};

export const streamPlaybackSource: PlaybackSource = {
  mode: 'stream',
  label: 'Streaming',
  urlForFile: (file) => file.stream_url ?? '',
};
