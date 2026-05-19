import { api } from '@/api/client';

export type PlaybackSourceMode = 'stream' | 'download';

export type PlaybackSource = {
  mode: PlaybackSourceMode;
  label: string;
  urlForFile: (bookId: string, fileIndex: number) => string;
};

export const streamPlaybackSource: PlaybackSource = {
  mode: 'stream',
  label: 'Streaming',
  urlForFile: (bookId, fileIndex) => api.streamUrl(bookId, fileIndex),
};
