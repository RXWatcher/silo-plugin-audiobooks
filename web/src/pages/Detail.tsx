import { useState } from 'react';
import { useParams } from 'react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Bookmark as BookmarkIcon, Link2, Search } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import AudioPlayer from '@/components/player/AudioPlayer';
import ChapterList from '@/components/ChapterList';
import BookmarkList from '@/components/BookmarkList';
import { Skeleton } from '@/components/ui/skeleton';
import { usePlayback } from '@/player/PlaybackProvider';

export default function Detail() {
  const { id = '' } = useParams();
  const qc = useQueryClient();
  const [chapterQuery, setChapterQuery] = useState('');
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
          {a.description && <p className="text-muted-foreground text-sm">{a.description}</p>}
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
