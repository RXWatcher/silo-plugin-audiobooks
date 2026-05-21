import { useEffect, useState } from 'react';
import {
  Activity,
  Bookmark,
  ChevronLeft,
  ChevronRight,
  Clock,
  Copy,
  Download,
  Gauge,
  Loader2,
  Pause,
  Play,
  RotateCcw,
  RotateCw,
  SkipBack,
  SkipForward,
  TimerOff,
  Trash2,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import type { AudiobookChapter, AudiobookDetail } from '@/api/types';
import {
  SLEEP_MINUTES,
  SKIP_INTERVALS,
  SPEEDS,
  VOICE_BOOSTS,
  EQ_PRESETS,
  usePlayback,
} from '@/player/PlaybackProvider';

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

export default function AudioPlayer({
  audiobook,
  initialPosition,
  onBookmark,
  seekRequest,
}: {
  audiobook: AudiobookDetail;
  initialPosition?: number;
  onBookmark?: (position: number, chapter?: AudiobookChapter) => void;
  seekRequest?: { id: number; position: number };
}) {
  const playback = usePlayback();
  const active = playback.isCurrentBook(audiobook.id);
  // Seven selects + checkboxes is a wall of secondary controls regardless of
  // viewport width. Speed is the dominant choice, so it stays inline; the rest
  // collapses behind a "More" toggle on every screen size.
  const [showMore, setShowMore] = useState(false);

  useEffect(() => {
    playback.startBook(audiobook, initialPosition);
    // Server progress is canonical only when this detail page loads a new book.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [audiobook.id]);

  useEffect(() => {
    if (!seekRequest) return;
    playback.seek(seekRequest.position, playback.playing);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seekRequest?.id]);

  if (!active) {
    return <div className="rounded-2xl border bg-surface p-4 text-sm">Loading player...</div>;
  }

  return (
    <div className="rounded-2xl border bg-surface p-4">
      <div className="grid gap-4 lg:grid-cols-[1fr_auto] lg:items-center">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-medium">
            {playback.buffering ? <Loader2 className="size-4 animate-spin" /> : null}
            <span className="truncate">{playback.activeChapter?.title || audiobook.title}</span>
          </div>
          <div className="text-muted-foreground mt-1 text-xs tabular-nums">
            {fmt(playback.bookTime)} / {fmt(playback.duration)} · {fmt(playback.remaining)} left
          </div>
          {playback.error ? (
            <div className="text-destructive mt-2 text-xs">{playback.error}</div>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {/*
            Chapter prev/next is one of the top three audiobook actions — it
            used to live only in the separate ChapterList component, forcing
            users to scroll during playback. The seek targets are derived
            from the same chapters array PlaybackProvider already exposes via
            activeChapter; we look up the neighbour by adjacency in time.
          */}
          {audiobook.chapters?.length ? (
            <Button
              size="icon"
              variant="ghost"
              onClick={() => {
                const chapters = audiobook.chapters ?? [];
                const idx = chapters.findIndex(
                  (c) => c.start_seconds === playback.activeChapter?.start_seconds,
                );
                const prev = idx > 0 ? chapters[idx - 1] : chapters[0];
                if (prev) playback.seek(prev.start_seconds, playback.playing);
              }}
              aria-label="Previous chapter"
              title="Previous chapter"
            >
              <ChevronLeft className="size-5" />
            </Button>
          ) : null}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => playback.seek(playback.bookTime - playback.skipSeconds)}
            aria-label={`Back ${playback.skipSeconds}s`}
          >
            <RotateCcw className="size-5" />
          </Button>
          <Button
            size="icon"
            onClick={playback.toggle}
            aria-label={playback.playing ? 'Pause' : 'Play'}
            className="size-12 rounded-full"
          >
            {playback.playing ? <Pause className="size-6" /> : <Play className="size-6" />}
          </Button>
          <Button
            size="icon"
            variant="ghost"
            onClick={() => playback.seek(playback.bookTime + playback.skipSeconds)}
            aria-label={`Forward ${playback.skipSeconds}s`}
          >
            <RotateCw className="size-5" />
          </Button>
          {audiobook.chapters?.length ? (
            <Button
              size="icon"
              variant="ghost"
              onClick={() => {
                const chapters = audiobook.chapters ?? [];
                const idx = chapters.findIndex(
                  (c) => c.start_seconds === playback.activeChapter?.start_seconds,
                );
                const next = idx >= 0 && idx < chapters.length - 1 ? chapters[idx + 1] : null;
                if (next) playback.seek(next.start_seconds, playback.playing);
              }}
              aria-label="Next chapter"
              title="Next chapter"
            >
              <ChevronRight className="size-5" />
            </Button>
          ) : null}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => playback.seek(0, false)}
            aria-label="Start book"
          >
            <SkipBack className="size-4" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            onClick={() => playback.seek(playback.duration, false)}
            aria-label="End book"
          >
            <SkipForward className="size-4" />
          </Button>
          {onBookmark ? (
            <Button
              size="icon"
              variant="ghost"
              onClick={() => onBookmark(playback.bookTime, playback.activeChapter)}
              aria-label="Bookmark position"
            >
              <Bookmark className="size-4" />
            </Button>
          ) : null}
        </div>
      </div>

      <div className="mt-4">
        <input
          type="range"
          min={0}
          max={Math.max(1, playback.duration)}
          value={playback.bookTime}
          onChange={(event) => playback.seek(Number(event.target.value), playback.playing)}
          className="w-full"
          aria-label="Audiobook position"
        />
        <div className="mt-3 flex flex-wrap items-center gap-3 text-xs">
          <label className="flex items-center gap-1">
            <Gauge className="text-muted-foreground size-4" />
            <select
              value={playback.speed}
              onChange={(event) => playback.setSpeed(Number(event.target.value))}
              className="rounded border bg-background px-2 py-1"
            >
              {SPEEDS.map((speed) => (
                <option key={speed} value={speed}>
                  {speed}x
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={() => setShowMore((v) => !v)}
            aria-expanded={showMore}
            className="rounded border bg-background px-2 py-1 text-xs"
          >
            {showMore ? 'Less' : 'More controls'}
          </button>
          <div className={showMore ? 'contents' : 'hidden'}>
          <label className="flex items-center gap-1">
            <Clock className="text-muted-foreground size-4" />
            <select
              value={playback.skipSeconds}
              onChange={(event) => playback.setSkipSeconds(Number(event.target.value))}
              className="rounded border bg-background px-2 py-1"
            >
              {SKIP_INTERVALS.map((seconds) => (
                <option key={seconds} value={seconds}>
                  {seconds}s skip
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1">
            {playback.sleepUntil == null ? (
              <TimerOff className="text-muted-foreground size-4" />
            ) : (
              <Clock className="text-muted-foreground size-4" />
            )}
            <select
              value={
                playback.sleepAtChapterEnd
                  ? -1
                  : playback.sleepUntil == null
                    ? 0
                    : Math.max(1, Math.round((playback.sleepUntil - Date.now()) / 60_000))
              }
              onChange={(event) => {
                const minutes = Number(event.target.value);
                if (minutes === -1) {
                  playback.setSleepAtChapterEnd(true);
                } else {
                  playback.setSleepMinutes(minutes);
                }
              }}
              className="rounded border bg-background px-2 py-1"
            >
              <option value={-1}>End of chapter</option>
              {SLEEP_MINUTES.map((minutes) => (
                <option key={minutes} value={minutes}>
                  {minutes === 0 ? 'No timer' : `${minutes} min`}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1">
            <Activity className="text-muted-foreground size-4" />
            <select
              value={playback.eqPreset}
              onChange={(event) => playback.setEqPreset(event.target.value as typeof playback.eqPreset)}
              className="rounded border bg-background px-2 py-1"
            >
              {EQ_PRESETS.map((preset) => (
                <option key={preset.id} value={preset.id}>
                  {preset.label}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1">
            <select
              value={playback.voiceBoost}
              onChange={(event) => playback.setVoiceBoost(Number(event.target.value))}
              className="rounded border bg-background px-2 py-1"
            >
              {VOICE_BOOSTS.map((boost) => (
                <option key={boost} value={boost}>
                  {boost === 1 ? 'Normal voice' : `${boost}x voice`}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1">
            <input
              type="checkbox"
              checked={playback.silenceTrim}
              onChange={(event) => playback.setSilenceTrim(event.target.checked)}
            />
            Trim silence
          </label>
          <Button
            size="sm"
            variant={playback.sourceMode === 'download' ? 'secondary' : 'ghost'}
            disabled={playback.downloading}
            onClick={() => {
              if (playback.downloaded) playback.setSourceMode(playback.sourceMode === 'download' ? 'stream' : 'download');
              else void playback.downloadBook();
            }}
          >
            <Download className="size-4" />
            {playback.downloading
              ? `${Math.round(playback.downloadProgress * 100)}%`
              : playback.sourceMode === 'download'
                ? 'Downloaded'
                : playback.downloaded
                  ? 'Use download'
                  : 'Download'}
          </Button>
          {playback.downloaded ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => void playback.deleteDownload()}
            >
              <Trash2 className="size-4" />
              Delete download
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              const url = new URL(window.location.href);
              url.searchParams.set('t', String(Math.floor(playback.bookTime)));
              void navigator.clipboard?.writeText(url.toString());
            }}
          >
            <Copy className="size-4" />
            Clip
          </Button>
          </div>
          <span className="text-muted-foreground ml-auto tabular-nums">
            Track {playback.activeFileOrdinal + 1}/{audiobook.files.length} ·{' '}
            {Math.round(playback.progressPct * 100)}%
          </span>
        </div>
        {playback.downloadError ? (
          <div className="text-destructive mt-2 text-xs">
            Download failed: {playback.downloadError}
          </div>
        ) : null}
        {/*
          The previous Diagnostics <details> block exposed session ids, raw
          time integers, and EQ preset internals to every user. That belonged
          in DevTools, not the shipped UI — removed for v1. Operators with a
          legitimate need for these fields can read them from the network tab
          or the player Redux-equivalent (PlaybackProvider's exported state).
        */}
      </div>
    </div>
  );
}
