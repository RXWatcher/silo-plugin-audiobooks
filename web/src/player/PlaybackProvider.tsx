import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { Link } from 'react-router';
import {
  Clock,
  Loader2,
  Pause,
  Play,
  Square,
  X,
} from 'lucide-react';
import { api } from '@/api/client';
import type { AudiobookChapter, AudiobookDetail } from '@/api/types';
import { Button } from '@/components/ui/button';
import {
  downloadOfflineFile,
  getOfflineBlob,
  hasOfflineBlob,
} from '@/player/offlineStore';
import { streamPlaybackSource } from '@/player/sources';
import {
  buildFileTimeline,
  chapterAt,
  fileTimeToBookTime,
  positionToFileTime,
  timelineDuration,
} from '@/player/timeline';

const SYNC_INTERVAL_MS = 15_000;
const STATS_KEY = 'audiobooks.listenStats';

export const SPEEDS = [0.8, 1.0, 1.25, 1.5, 1.75, 2.0];
export const SKIP_INTERVALS = [15, 30, 45, 60];
export const SLEEP_MINUTES = [0, 15, 30, 45, 60];
export const VOICE_BOOSTS = [1, 1.25, 1.5, 2];

declare global {
  interface Window {
    webkitAudioContext?: typeof AudioContext;
  }
}

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

function prefNumber(key: string, fallback: number): number {
  const value = window.localStorage.getItem(key);
  return value == null ? fallback : Number(value) || fallback;
}

function bookPrefKey(bookId: string, name: string): string {
  return `audiobooks.${bookId}.${name}`;
}

function readStats(): Record<string, number> {
  try {
    return JSON.parse(window.localStorage.getItem(STATS_KEY) || '{}') as Record<string, number>;
  } catch {
    return {};
  }
}

type PlaybackContextValue = {
  activeChapter?: AudiobookChapter;
  activeFileOrdinal: number;
  audiobook?: AudiobookDetail;
  bookTime: number;
  buffering: boolean;
  duration: number;
  error: string;
  lastSyncAt: number;
  listenedSeconds: number;
  isCurrentBook: (bookId: string) => boolean;
  playing: boolean;
  progressPct: number;
  remaining: number;
  sessionId: string | null;
  seek: (position: number, keepPlaying?: boolean) => void;
  setSkipSeconds: (seconds: number) => void;
  setSleepAtChapterEnd: (enabled: boolean) => void;
  setSleepMinutes: (minutes: number) => void;
  setSpeed: (speed: number) => void;
  setSilenceTrim: (enabled: boolean) => void;
  setSourceMode: (mode: 'stream' | 'download') => void;
  setVoiceBoost: (boost: number) => void;
  skipSeconds: number;
  silenceTrim: boolean;
  sleepAtChapterEnd: boolean;
  sleepUntil: number | null;
  sourceLabel: string;
  sourceMode: 'stream' | 'download';
  speed: number;
  startBook: (audiobook: AudiobookDetail, initialPosition?: number) => void;
  stop: () => void;
  toggle: () => void;
  voiceBoost: number;
  downloadBook: () => Promise<void>;
  downloading: boolean;
  downloaded: boolean;
};

const PlaybackContext = createContext<PlaybackContextValue | null>(null);

export function usePlayback() {
  const ctx = useContext(PlaybackContext);
  if (!ctx) throw new Error('usePlayback must be used within PlaybackProvider');
  return ctx;
}

export function PlaybackProvider({ children }: { children: ReactNode }) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const sessionId = useRef<string | null>(null);
  const pendingFileTime = useRef<number | null>(null);
  const shouldPlayAfterLoad = useRef(false);
  const lastSync = useRef(0);
  const bookTimeRef = useRef(0);
  const pausedAt = useRef<number | null>(null);
  const currentBookRef = useRef<AudiobookDetail | undefined>(undefined);
  const objectUrlRef = useRef<string | null>(null);
  const unsyncedListened = useRef(0);
  const audioContextRef = useRef<AudioContext | null>(null);
  const analyserRef = useRef<AnalyserNode | null>(null);
  const gainRef = useRef<GainNode | null>(null);

  const [audiobook, setAudiobook] = useState<AudiobookDetail>();
  const [bookTime, setBookTime] = useState(0);
  const [fileOrdinal, setFileOrdinal] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeedState] = useState(() => prefNumber('audiobooks.speed', 1));
  const [skipSeconds, setSkipSecondsState] = useState(() =>
    prefNumber('audiobooks.skipSeconds', 30),
  );
  const [sleepUntil, setSleepUntil] = useState<number | null>(null);
  const [sleepAtChapterEnd, setSleepAtChapterEnd] = useState(false);
  const [buffering, setBuffering] = useState(false);
  const [error, setError] = useState('');
  const [lastSyncAt, setLastSyncAt] = useState(0);
  const [listenedSeconds, setListenedSeconds] = useState(0);
  const [voiceBoost, setVoiceBoostState] = useState(() => prefNumber('audiobooks.voiceBoost', 1));
  const [silenceTrim, setSilenceTrimState] = useState(
    () => window.localStorage.getItem('audiobooks.silenceTrim') === 'true',
  );
  const [sourceMode, setSourceMode] = useState<'stream' | 'download'>('stream');
  const [sourceUrl, setSourceUrl] = useState('');
  const [downloading, setDownloading] = useState(false);
  const [downloaded, setDownloaded] = useState(false);

  const timeline = useMemo(() => buildFileTimeline(audiobook?.files ?? []), [audiobook]);
  const duration = audiobook?.duration_seconds || timelineDuration(timeline);
  const activeFile = timeline[fileOrdinal] ?? timeline[0];
  const activeChapter = chapterAt(audiobook?.chapters, bookTime);
  const remaining = Math.max(0, duration - bookTime);
  const progressPct = duration > 0 ? bookTime / duration : 0;

  useEffect(() => {
    bookTimeRef.current = bookTime;
  }, [bookTime]);

  const sync = useCallback(
    async (position = bookTimeRef.current, final = false) => {
      const sid = sessionId.current;
      if (!sid) return;
      const clamped = Math.floor(Math.max(0, Math.min(position, duration)));
      const body = {
        current_seconds: clamped,
        duration_seconds: Math.floor(duration),
        is_finished: duration > 0 ? clamped / duration >= 0.95 : false,
        time_listened_seconds: unsyncedListened.current,
      };
      try {
        if (final) await api.closePlaybackSession(sid, body);
        else await api.updatePlaybackSession(sid, body);
        unsyncedListened.current = 0;
        lastSync.current = Date.now();
        setLastSyncAt(lastSync.current);
      } catch {
        // Keep playback alive if a background sync fails.
      }
    },
    [duration],
  );

  const closeCurrentSession = useCallback(
    async (final = true) => {
      const sid = sessionId.current;
      sessionId.current = null;
      if (!sid) return;
      await api
        .closePlaybackSession(sid, {
          current_seconds: Math.floor(bookTimeRef.current),
          duration_seconds: Math.floor(duration),
          is_finished: duration > 0 ? bookTimeRef.current / duration >= 0.95 : false,
          time_listened_seconds: unsyncedListened.current,
        })
        .then(() => {
          unsyncedListened.current = 0;
        })
        .catch(() => {
          if (!final) return;
        });
    },
    [duration],
  );

  const seek = useCallback(
    (next: number, keepPlaying = playing) => {
      if (!audiobook || timeline.length === 0) return;
      const target = Math.max(0, Math.min(next, duration));
      const filePos = positionToFileTime(timeline, target);
      pendingFileTime.current = filePos.fileTime;
      shouldPlayAfterLoad.current = keepPlaying;
      setBookTime(target);
      setFileOrdinal(filePos.fileOrdinal);
      if (audioRef.current && filePos.fileOrdinal === fileOrdinal) {
        audioRef.current.currentTime = filePos.fileTime;
        if (keepPlaying) void audioRef.current.play().catch(() => setPlaying(false));
      }
      void sync(target, false);
    },
    [audiobook, duration, fileOrdinal, playing, sync, timeline],
  );

  const startBook = useCallback(
    (nextBook: AudiobookDetail, initialPosition = 0) => {
      if (currentBookRef.current?.id === nextBook.id) {
        setAudiobook(nextBook);
        currentBookRef.current = nextBook;
        return;
      }

      void closeCurrentSession(false);
      audioRef.current?.pause();
      currentBookRef.current = nextBook;
      const nextTimeline = buildFileTimeline(nextBook.files);
      const nextDuration = nextBook.duration_seconds || timelineDuration(nextTimeline);
      const resume = Math.max(0, Math.min(initialPosition, nextDuration));
      const filePos = positionToFileTime(nextTimeline, resume);
      pendingFileTime.current = filePos.fileTime;
      shouldPlayAfterLoad.current = false;
      setAudiobook(nextBook);
      setBookTime(resume);
      setFileOrdinal(filePos.fileOrdinal);
      setPlaying(false);
      setBuffering(false);
      setError('');
      setLastSyncAt(0);
      setListenedSeconds(readStats()[nextBook.id] ?? 0);
      setSpeedState(prefNumber(bookPrefKey(nextBook.id, 'speed'), prefNumber('audiobooks.speed', 1)));
      setSkipSecondsState(
        prefNumber(bookPrefKey(nextBook.id, 'skipSeconds'), prefNumber('audiobooks.skipSeconds', 30)),
      );
      setVoiceBoostState(
        prefNumber(bookPrefKey(nextBook.id, 'voiceBoost'), prefNumber('audiobooks.voiceBoost', 1)),
      );
      setSilenceTrimState(
        window.localStorage.getItem(bookPrefKey(nextBook.id, 'silenceTrim')) === 'true' ||
          window.localStorage.getItem('audiobooks.silenceTrim') === 'true',
      );
      api
        .getListeningStats(nextBook.id)
        .then((stats) => setListenedSeconds(stats.listened_seconds ?? 0))
        .catch(() => {});
      void Promise.all(nextBook.files.map((file) => hasOfflineBlob(nextBook.id, file.index))).then(
        (results) => setDownloaded(results.length > 0 && results.every(Boolean)),
      );
      api
        .createPlaybackSession(nextBook.id, {
          current_seconds: Math.floor(resume),
          device_id: 'continuum-web',
          device_info: { userAgent: navigator.userAgent },
        })
        .then((session) => {
          if (currentBookRef.current?.id === nextBook.id) sessionId.current = session.id;
        })
        .catch(() => setError('Could not start a playback session.'));
    },
    [closeCurrentSession],
  );

  const stop = useCallback(() => {
    audioRef.current?.pause();
    void closeCurrentSession(true);
    sessionId.current = null;
    currentBookRef.current = undefined;
    setAudiobook(undefined);
    setBookTime(0);
    setFileOrdinal(0);
    setPlaying(false);
    setBuffering(false);
    setError('');
    setLastSyncAt(0);
  }, [closeCurrentSession]);

  const toggle = useCallback(() => {
    if (!audioRef.current) return;
    if (audioRef.current.paused) void audioRef.current.play();
    else audioRef.current.pause();
  }, []);

  const setSpeed = useCallback((next: number) => {
    setSpeedState(next);
    window.localStorage.setItem('audiobooks.speed', String(next));
    if (currentBookRef.current) {
      window.localStorage.setItem(bookPrefKey(currentBookRef.current.id, 'speed'), String(next));
    }
    if (audioRef.current) audioRef.current.playbackRate = next;
  }, []);

  const setSkipSeconds = useCallback((next: number) => {
    setSkipSecondsState(next);
    window.localStorage.setItem('audiobooks.skipSeconds', String(next));
    if (currentBookRef.current) {
      window.localStorage.setItem(bookPrefKey(currentBookRef.current.id, 'skipSeconds'), String(next));
    }
  }, []);

  const setVoiceBoost = useCallback((next: number) => {
    setVoiceBoostState(next);
    window.localStorage.setItem('audiobooks.voiceBoost', String(next));
    if (currentBookRef.current) {
      window.localStorage.setItem(bookPrefKey(currentBookRef.current.id, 'voiceBoost'), String(next));
    }
    if (gainRef.current) gainRef.current.gain.value = next;
  }, []);

  const setSilenceTrim = useCallback(
    (enabled: boolean) => {
      setSilenceTrimState(enabled);
      window.localStorage.setItem('audiobooks.silenceTrim', String(enabled));
      if (currentBookRef.current) {
        window.localStorage.setItem(bookPrefKey(currentBookRef.current.id, 'silenceTrim'), String(enabled));
      }
      if (!enabled && audioRef.current) audioRef.current.playbackRate = speed;
    },
    [speed],
  );

  useEffect(() => {
    if (audioRef.current && !silenceTrim) audioRef.current.playbackRate = speed;
    if (gainRef.current) gainRef.current.gain.value = voiceBoost;
  }, [silenceTrim, speed, voiceBoost]);

  const setSleepMinutes = useCallback((minutes: number) => {
    setSleepAtChapterEnd(false);
    setSleepUntil(minutes > 0 ? Date.now() + minutes * 60_000 : null);
  }, []);

  const setSleepAtChapterEndMode = useCallback((enabled: boolean) => {
    setSleepAtChapterEnd(enabled);
    if (enabled) setSleepUntil(null);
  }, []);

  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      if (Date.now() - lastSync.current >= SYNC_INTERVAL_MS) void sync(bookTimeRef.current, false);
    }, 5_000);
    return () => window.clearInterval(id);
  }, [playing, sync]);

  useEffect(() => {
    if (sleepUntil == null) return;
    const id = window.setInterval(() => {
      if (Date.now() >= sleepUntil) {
        audioRef.current?.pause();
        setSleepUntil(null);
      }
    }, 1_000);
    return () => window.clearInterval(id);
  }, [sleepUntil]);

  useEffect(() => {
    if (!playing || !audiobook) return;
    const id = window.setInterval(() => {
      setListenedSeconds((value) => {
        const next = value + 1;
        unsyncedListened.current += 1;
        const stats = readStats();
        stats[audiobook.id] = next;
        window.localStorage.setItem(STATS_KEY, JSON.stringify(stats));
        return next;
      });
    }, 1_000);
    return () => window.clearInterval(id);
  }, [audiobook, playing]);

  const ensureAudioGraph = useCallback(() => {
    const audio = audioRef.current;
    if (!audio || audioContextRef.current) return;
    const AudioContextCtor = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextCtor) return;
    try {
      const context = new AudioContextCtor();
      const source = context.createMediaElementSource(audio);
      const analyser = context.createAnalyser();
      const gain = context.createGain();
      analyser.fftSize = 256;
      gain.gain.value = voiceBoost;
      source.connect(analyser);
      analyser.connect(gain);
      gain.connect(context.destination);
      audioContextRef.current = context;
      analyserRef.current = analyser;
      gainRef.current = gain;
    } catch {
      // Some browsers reject media element graphs for protected streams.
    }
  }, [voiceBoost]);

  useEffect(() => {
    if (!playing || !silenceTrim || !analyserRef.current || !audioRef.current) return;
    const analyser = analyserRef.current;
    const data = new Uint8Array(analyser.fftSize);
    const id = window.setInterval(() => {
      analyser.getByteTimeDomainData(data);
      let sum = 0;
      for (const value of data) {
        const centered = (value - 128) / 128;
        sum += centered * centered;
      }
      const rms = Math.sqrt(sum / data.length);
      audioRef.current!.playbackRate = rms < 0.018 ? Math.max(speed, 2.5) : speed;
    }, 250);
    return () => {
      window.clearInterval(id);
      if (audioRef.current) audioRef.current.playbackRate = speed;
    };
  }, [playing, silenceTrim, speed]);

  useEffect(() => {
    if (!audiobook || !activeFile) {
      setSourceUrl('');
      return;
    }
    let cancelled = false;
    const previous = objectUrlRef.current;
    objectUrlRef.current = null;
    if (previous) URL.revokeObjectURL(previous);

    if (sourceMode === 'download') {
      getOfflineBlob(audiobook.id, activeFile.fileIndex)
        .then((blob) => {
          if (cancelled) return;
          if (!blob) {
            setSourceMode('stream');
            setSourceUrl(streamPlaybackSource.urlForFile(audiobook.id, activeFile.fileIndex));
            return;
          }
          const url = URL.createObjectURL(blob);
          objectUrlRef.current = url;
          setSourceUrl(url);
        })
        .catch(() => {
          if (!cancelled) {
            setSourceMode('stream');
            setSourceUrl(streamPlaybackSource.urlForFile(audiobook.id, activeFile.fileIndex));
          }
        });
    } else {
      setSourceUrl(streamPlaybackSource.urlForFile(audiobook.id, activeFile.fileIndex));
    }
    return () => {
      cancelled = true;
    };
  }, [activeFile, audiobook, sourceMode]);

  const downloadBook = useCallback(async () => {
    if (!audiobook) return;
    setDownloading(true);
    try {
      for (const file of audiobook.files) {
        await downloadOfflineFile(
          audiobook.id,
          file.index,
          streamPlaybackSource.urlForFile(audiobook.id, file.index),
        );
      }
      setDownloaded(true);
      setSourceMode('download');
    } finally {
      setDownloading(false);
    }
  }, [audiobook]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (
        target?.tagName === 'INPUT' ||
        target?.tagName === 'SELECT' ||
        target?.tagName === 'TEXTAREA'
      ) {
        return;
      }
      if (event.code === 'Space') {
        event.preventDefault();
        toggle();
      } else if (event.key === 'ArrowLeft') {
        event.preventDefault();
        seek(bookTimeRef.current - skipSeconds);
      } else if (event.key === 'ArrowRight') {
        event.preventDefault();
        seek(bookTimeRef.current + skipSeconds);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [seek, skipSeconds, toggle]);

  useEffect(() => {
    const onHidden = () => {
      if (document.visibilityState === 'hidden') void sync(bookTimeRef.current, false);
    };
    document.addEventListener('visibilitychange', onHidden);
    return () => document.removeEventListener('visibilitychange', onHidden);
  }, [sync]);

  useEffect(() => {
    if (!audiobook || !('mediaSession' in navigator)) return;
    navigator.mediaSession.metadata = new MediaMetadata({
      title: audiobook.title,
      artist: audiobook.authors?.join(', ') || audiobook.narrators?.join(', ') || '',
      album: audiobook.series || '',
      artwork: audiobook.cover_url ? [{ src: audiobook.cover_url }] : [],
    });
    navigator.mediaSession.setActionHandler('play', () => void audioRef.current?.play());
    navigator.mediaSession.setActionHandler('pause', () => audioRef.current?.pause());
    navigator.mediaSession.setActionHandler('seekbackward', () =>
      seek(bookTimeRef.current - skipSeconds),
    );
    navigator.mediaSession.setActionHandler('seekforward', () =>
      seek(bookTimeRef.current + skipSeconds),
    );
    navigator.mediaSession.setActionHandler('seekto', (details) => {
      if (details.seekTime != null) seek(details.seekTime);
    });
  }, [audiobook, seek, skipSeconds]);

  useEffect(() => {
    return () => {
      if (objectUrlRef.current) URL.revokeObjectURL(objectUrlRef.current);
      void closeCurrentSession(true);
    };
  }, [closeCurrentSession]);

  const value = useMemo<PlaybackContextValue>(
    () => ({
      activeChapter,
      activeFileOrdinal: fileOrdinal,
      audiobook,
      bookTime,
      buffering,
      duration,
	      error,
	      lastSyncAt,
	      listenedSeconds,
	      isCurrentBook: (bookId: string) => audiobook?.id === bookId,
	      playing,
	      progressPct,
	      remaining,
	      sessionId: sessionId.current,
	      seek,
	      setSkipSeconds,
	      setSleepAtChapterEnd: setSleepAtChapterEndMode,
	      setSleepMinutes,
	      setSpeed,
      setSilenceTrim,
      setSourceMode,
      setVoiceBoost,
	      skipSeconds,
	      silenceTrim,
	      sleepAtChapterEnd,
	      sleepUntil,
      sourceMode,
      sourceLabel: sourceMode === 'download' ? 'Downloaded' : streamPlaybackSource.label,
	      speed,
	      startBook,
	      stop,
      toggle,
      voiceBoost,
      downloadBook,
      downloading,
      downloaded,
	    }),
    [
      activeChapter,
      audiobook,
      bookTime,
      buffering,
      duration,
	      error,
	      lastSyncAt,
	      listenedSeconds,
	      fileOrdinal,
	      playing,
      progressPct,
      remaining,
      seek,
      setSkipSeconds,
	      setSleepAtChapterEndMode,
	      setSleepMinutes,
	      setSpeed,
      setSilenceTrim,
      sourceMode,
      setVoiceBoost,
	      skipSeconds,
	      silenceTrim,
	      sleepAtChapterEnd,
	      sleepUntil,
	      speed,
      startBook,
	      stop,
	      toggle,
      voiceBoost,
      downloadBook,
      downloading,
      downloaded,
	    ],
	  );

  return (
    <PlaybackContext.Provider value={value}>
      {activeFile && audiobook ? (
        <audio
          ref={audioRef}
          src={sourceUrl || streamPlaybackSource.urlForFile(audiobook.id, activeFile.fileIndex)}
          preload="metadata"
          onLoadedMetadata={(event) => {
            const audio = event.currentTarget;
            audio.playbackRate = speed;
            ensureAudioGraph();
            if (pendingFileTime.current != null) {
              audio.currentTime = pendingFileTime.current;
              pendingFileTime.current = null;
            }
            if (shouldPlayAfterLoad.current) {
              shouldPlayAfterLoad.current = false;
              void audio.play().catch(() => setPlaying(false));
            }
          }}
          onTimeUpdate={(event) => {
            const next = fileTimeToBookTime(timeline, fileOrdinal, event.currentTarget.currentTime);
            setBookTime(next);
            const chapter = chapterAt(audiobook.chapters, next);
            if (sleepAtChapterEnd && chapter && next >= chapter.end_seconds - 1) {
              event.currentTarget.pause();
              setSleepAtChapterEnd(false);
            }
          }}
          onWaiting={() => setBuffering(true)}
          onCanPlay={() => setBuffering(false)}
          onPlay={() => {
            ensureAudioGraph();
            void audioContextRef.current?.resume();
            if (pausedAt.current && Date.now() - pausedAt.current > 60_000) {
              seek(Math.max(0, bookTimeRef.current - 15), true);
            }
            pausedAt.current = null;
            setPlaying(true);
            setError('');
          }}
          onPause={() => {
            setPlaying(false);
            pausedAt.current = Date.now();
            void sync(bookTimeRef.current, false);
          }}
          onError={() => {
            setPlaying(false);
            setBuffering(false);
            setError('Playback failed. Try again or reload the stream.');
          }}
          onEnded={() => {
            const next = timeline[fileOrdinal + 1];
            if (next) {
              pendingFileTime.current = 0;
              shouldPlayAfterLoad.current = true;
              setFileOrdinal(fileOrdinal + 1);
              setBookTime(next.start);
              void sync(next.start, false);
            } else {
              setPlaying(false);
              setBookTime(duration);
              void sync(duration, false);
            }
          }}
        />
      ) : null}
      {children}
      <MiniPlayer />
    </PlaybackContext.Provider>
  );
}

function MiniPlayer() {
  const playback = usePlayback();
  const audiobook = playback.audiobook;
  if (!audiobook) return null;

  return (
    <div className="fixed inset-x-3 bottom-3 z-40 rounded-xl border border-border bg-background/95 p-3 shadow-lg backdrop-blur md:left-auto md:w-[28rem]">
      <div className="flex items-center gap-3">
        {audiobook.cover_url ? (
          <img src={audiobook.cover_url} alt="" className="size-10 rounded object-cover" />
        ) : null}
        <Link to={`/audiobook/${encodeURIComponent(audiobook.id)}`} className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{audiobook.title}</div>
          <div className="text-muted-foreground flex items-center gap-1 truncate text-xs">
            {playback.buffering ? <Loader2 className="size-3 animate-spin" /> : null}
            <span className="truncate">
              {playback.activeChapter?.title || fmt(playback.bookTime)}
            </span>
          </div>
          <div className="mt-1 h-1 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full bg-primary"
              style={{ width: `${Math.max(0, Math.min(100, playback.progressPct * 100))}%` }}
            />
          </div>
        </Link>
        <Button size="icon-sm" onClick={playback.toggle} aria-label={playback.playing ? 'Pause' : 'Play'}>
          {playback.playing ? <Pause className="size-4" /> : <Play className="size-4" />}
        </Button>
        <Button size="icon-sm" variant="ghost" onClick={playback.stop} aria-label="Stop playback">
          <Square className="size-4" />
        </Button>
        <Button size="icon-sm" variant="ghost" onClick={playback.stop} aria-label="Dismiss player">
          <X className="size-4" />
        </Button>
      </div>
      {playback.error ? <div className="text-destructive mt-2 text-xs">{playback.error}</div> : null}
      {playback.sleepUntil || playback.sleepAtChapterEnd ? (
        <div className="text-muted-foreground mt-2 flex items-center gap-1 text-xs">
          <Clock className="size-3" />
          {playback.sleepAtChapterEnd
            ? 'Sleep at chapter end'
            : `Sleep in ${Math.max(1, Math.round(((playback.sleepUntil ?? Date.now()) - Date.now()) / 60_000))} min`}
        </div>
      ) : null}
    </div>
  );
}
