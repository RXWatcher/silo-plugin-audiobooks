import { useEffect, useState } from 'react';
import { useParams } from 'react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Bookmark as BookmarkIcon, Link2, Search, Scissors, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import AudioPlayer from '@/components/player/AudioPlayer';
import ChapterList from '@/components/ChapterList';
import BookmarkList from '@/components/BookmarkList';
import BookActivity from '@/components/BookActivity';
import { Skeleton } from '@/components/ui/skeleton';
import { hasHTML, renderDescriptionHTML } from '@/lib/description';
import { usePlayback } from '@/player/PlaybackProvider';

type Clip = {
  id: string;
  title: string;
  position: number;
  created_at: string;
};

function clipKey(bookId: string): string {
  return `audiobooks.${bookId}.clips`;
}

function readClips(bookId: string): Clip[] {
  try {
    return JSON.parse(window.localStorage.getItem(clipKey(bookId)) || '[]') as Clip[];
  } catch {
    return [];
  }
}

function writeClips(bookId: string, clips: Clip[]) {
  window.localStorage.setItem(clipKey(bookId), JSON.stringify(clips));
}

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

export default function Detail() {
  const { id = '' } = useParams();
  const qc = useQueryClient();
  const [chapterQuery, setChapterQuery] = useState('');
  const [clips, setClips] = useState<Clip[]>([]);
  const [seekRequest, setSeekRequest] = useState<{ id: number; position: number }>();
  const playback = usePlayback();
  const detail = useQuery({
    queryKey: ['audiobook', id],
    queryFn: () => api.getAudiobook(id),
    enabled: !!id,
  });
  const sessions = useQuery({
    queryKey: ['playback-sessions'],
    queryFn: () => api.listMyPlaybackSessions(),
    refetchInterval: 30_000,
  });

  const addBookmark = useMutation({
    mutationFn: (position: number) =>
      api.createBookmark(id, { position_seconds: Math.floor(position) }),
    onSuccess: () => {
      toast.success('Bookmark added');
      qc.invalidateQueries({ queryKey: ['audiobook', id] });
    },
    onError: (err) => toast.error(`Failed: ${err}`),
  });

  const removeBookmark = useMutation({
    mutationFn: (bmId: string) => api.deleteBookmark(id, bmId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['audiobook', id] }),
  });
  const closeSession = useMutation({
    mutationFn: (session: { id: string; current_time: number }) =>
      api.closePlaybackSession(session.id, { current_seconds: session.current_time }),
    onSuccess: () => sessions.refetch(),
  });

  useEffect(() => {
    if (id) setClips(readClips(id));
  }, [id]);

  if (detail.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }
  if (detail.error) {
    return <div className="text-destructive">Failed to load: {String(detail.error)}</div>;
  }
  if (!detail.data) return null;

  const a = detail.data.audiobook;
  const progress = detail.data.progress;
  const bookmarks = detail.data.bookmarks ?? [];
  const clipTarget = Number(new URLSearchParams(window.location.search).get('t') || '0') || 0;
  const initialPosition = clipTarget > 0 ? clipTarget : progress?.current_seconds;
  const livePosition = playback.isCurrentBook(a.id)
    ? playback.bookTime
    : (progress?.current_seconds ?? 0);
  const q = chapterQuery.trim().toLowerCase();
  const filteredChapters = q
    ? (a.chapters ?? []).filter((chapter) => chapter.title.toLowerCase().includes(q))
    : (a.chapters ?? []);
  const otherSessions =
    sessions.data?.items.filter(
      (session) => session.book_id === a.id && session.id !== playback.sessionId,
    ) ?? [];

  const saveClip = () => {
    const position = Math.floor(livePosition);
    const chapter = a.chapters?.find(
      (item) => position >= item.start_seconds && position < item.end_seconds,
    );
    const next = [
      {
        id: `${Date.now()}`,
        title: chapter?.title || `Clip at ${fmt(position)}`,
        position,
        created_at: new Date().toISOString(),
      },
      ...clips,
    ];
    setClips(next);
    writeClips(a.id, next);
  };

  const deleteClip = (clipId: string) => {
    const next = clips.filter((clip) => clip.id !== clipId);
    setClips(next);
    writeClips(a.id, next);
  };

  return (
    <div className="space-y-8">
      <header className="flex flex-col gap-6 sm:flex-row">
        <div className="bg-muted aspect-[2/3] w-48 shrink-0 overflow-hidden rounded-lg">
          {a.cover_url ? (
            <img src={a.cover_url} alt={a.title} className="size-full object-cover" />
          ) : null}
        </div>
        <div className="flex-1 space-y-3">
          <h1 className="text-3xl font-semibold leading-tight">{a.title}</h1>
          {a.authors && (
            <div className="text-muted-foreground">{a.authors.join(', ')}</div>
          )}
          {a.narrators && a.narrators.length > 0 && (
            <div className="text-muted-foreground text-sm">
              Narrated by {a.narrators.join(', ')}
            </div>
          )}
          {a.description && (
            hasHTML(a.description) ? (
              <div
                className="text-muted-foreground prose prose-sm prose-invert max-w-none text-sm"
                // Content is sanitised by DOMPurify in renderDescriptionHTML —
                // narrow allowlist of inline/block tags, href-only on <a>,
                // http(s)/mailto URL scheme enforced.
                dangerouslySetInnerHTML={{ __html: renderDescriptionHTML(a.description) }}
              />
            ) : (
              <p className="text-muted-foreground whitespace-pre-line text-sm">{a.description}</p>
            )
          )}
          <div className="text-muted-foreground flex flex-wrap gap-x-4 text-xs">
            {a.year ? <span>{a.year}</span> : null}
            {a.publisher ? <span>{a.publisher}</span> : null}
            {a.series ? (
              <span>
                {a.series}
                {a.series_position ? ` #${a.series_position}` : ''}
              </span>
            ) : null}
            {a.duration_seconds ? (
              <span>{Math.floor(a.duration_seconds / 3600)}h</span>
            ) : null}
          </div>
        </div>
      </header>

      {a.files.length > 0 && (
        <section className="space-y-2">
          {otherSessions.length > 0 ? (
            <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
              <div className="font-medium">This book is active on another device.</div>
              <div className="text-muted-foreground mt-1">
                Stop the other session before continuing here.
              </div>
              <Button
                size="sm"
                variant="outline"
                className="mt-3"
                onClick={() => {
                  void Promise.all(
                    otherSessions.map((session) =>
                      api.closePlaybackSession(session.id, {
                        current_seconds: session.current_time,
                      }),
                    ),
                  ).then(() => sessions.refetch());
                }}
              >
                Take over playback
              </Button>
            </div>
          ) : null}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
              Player
            </h2>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => addBookmark.mutate(livePosition)}
              >
                <BookmarkIcon className="mr-2 size-4" /> Bookmark
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  const url = new URL(window.location.href);
                  url.searchParams.set('t', String(Math.floor(livePosition)));
                  void navigator.clipboard?.writeText(url.toString());
                }}
              >
                <Link2 className="mr-2 size-4" /> Clip link
              </Button>
              <Button size="sm" variant="outline" onClick={saveClip}>
                <Scissors className="mr-2 size-4" /> Save clip
              </Button>
            </div>
          </div>
          <AudioPlayer
            audiobook={a}
            initialPosition={initialPosition}
            seekRequest={seekRequest}
            onBookmark={(position) => addBookmark.mutate(position)}
          />
        </section>
      )}

      {a.chapters && a.chapters.length > 0 && (
        <section>
          <div className="mb-2 flex items-center gap-2">
            <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
              Chapters
            </h2>
            <div className="ml-auto flex items-center gap-2">
              <Search className="text-muted-foreground size-4" />
              <input
                value={chapterQuery}
                onChange={(event) => setChapterQuery(event.target.value)}
                placeholder="Filter chapters"
                className="rounded border border-border bg-background px-2 py-1 text-sm"
              />
            </div>
          </div>
          <ChapterList
            chapters={filteredChapters}
            activePosition={livePosition}
            onSelect={(position) => {
              setSeekRequest({ id: Date.now(), position });
            }}
          />
        </section>
      )}

      <section>
        <h2 className="mb-2 text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Bookmarks
        </h2>
        <BookmarkList
          bookmarks={bookmarks}
          onDelete={(bmId) => removeBookmark.mutate(bmId)}
          onSelect={(position) => setSeekRequest({ id: Date.now(), position })}
        />
      </section>

      <section>
        <BookActivity bookId={id} />
      </section>

      <section>
        <h2 className="mb-2 text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Listening stats
        </h2>
        <div className="grid gap-2 sm:grid-cols-3">
          <div className="rounded-md border border-border bg-surface p-3 text-sm">
            <div className="text-muted-foreground text-xs">Listened</div>
            <div className="mt-1 font-medium tabular-nums">{fmt(playback.listenedSeconds)}</div>
          </div>
          <div className="rounded-md border border-border bg-surface p-3 text-sm">
            <div className="text-muted-foreground text-xs">Progress</div>
            <div className="mt-1 font-medium tabular-nums">
              {Math.round((playback.isCurrentBook(a.id) ? playback.progressPct : progress?.progress_pct ?? 0) * 100)}%
            </div>
          </div>
          <div className="rounded-md border border-border bg-surface p-3 text-sm">
            <div className="text-muted-foreground text-xs">Position</div>
            <div className="mt-1 font-medium tabular-nums">{fmt(livePosition)}</div>
          </div>
        </div>
      </section>

      <section>
        <h2 className="mb-2 text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Clips
        </h2>
        {clips.length ? (
          <div className="space-y-1">
            {clips.map((clip) => (
              <div
                key={clip.id}
                className="flex items-center justify-between rounded-md border border-border bg-surface px-3 py-2 text-sm"
              >
                <button
                  type="button"
                  onClick={() => setSeekRequest({ id: Date.now(), position: clip.position })}
                  className="min-w-0 flex-1 text-left"
                >
                  <div className="truncate font-medium">{clip.title}</div>
                  <div className="text-muted-foreground text-xs tabular-nums">
                    {fmt(clip.position)}
                  </div>
                </button>
                <div className="flex items-center gap-1">
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    onClick={() => {
                      const url = new URL(window.location.href);
                      url.searchParams.set('t', String(clip.position));
                      void navigator.clipboard?.writeText(url.toString());
                    }}
                    aria-label="Copy clip link"
                  >
                    <Link2 className="size-4" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    onClick={() => deleteClip(clip.id)}
                    aria-label="Delete clip"
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-muted-foreground text-sm">No saved clips yet.</div>
        )}
      </section>

      {sessions.data?.items?.length ? (
        <section>
          <h2 className="mb-2 text-sm font-medium uppercase tracking-wide text-muted-foreground">
            Active sessions
          </h2>
          <div className="space-y-1">
            {sessions.data.items.map((session) => (
              <div
                key={session.id}
                className="flex items-center justify-between rounded-md border border-border bg-surface px-3 py-2 text-sm"
              >
                <div className="min-w-0">
                  <div className="truncate font-medium">
                    {session.media_player || session.device_id || 'Audiobook player'}
                  </div>
                  <div className="text-muted-foreground text-xs">
                    {session.book_id} · {Math.floor(session.current_time)}s
                  </div>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => closeSession.mutate(session)}
                >
                  Stop
                </Button>
              </div>
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}
