import { Link, useSearchParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Mic, Rss } from 'lucide-react';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import type { LibraryInfo } from '@/api/types';

// Podcasts is the landing page for podcast libraries. Listeners select a
// library pill (when more than one podcast library exists) and the grid
// shows every podcast in that library. Single-library deployments skip
// the pill row.
export default function Podcasts() {
  const [params] = useSearchParams();
  const libraryID = Number(params.get('library_id') || 0) || undefined;

  const libraries = useQuery({
    queryKey: ['libraries'],
    queryFn: () => api.listLibraries(),
  });
  const podcastLibraries = (libraries.data?.items ?? []).filter(
    (l: LibraryInfo) => l.media_type === 'podcast',
  );

  const podcasts = useQuery({
    queryKey: ['podcasts', libraryID],
    queryFn: () => api.listPodcasts(libraryID),
    enabled: libraries.isSuccess,
  });

  if (libraries.isLoading || podcasts.isLoading)
    return (
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
        {Array.from({ length: 8 }).map((_, i) => (
          <Skeleton key={i} className="aspect-square w-full" />
        ))}
      </div>
    );

  const items = podcasts.data?.items ?? [];
  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-semibold tracking-tight">Podcasts</h2>
      </div>

      {podcastLibraries.length > 1 && (
        <div className="flex flex-wrap gap-2">
          <PodcastPill to="/podcasts" active={!libraryID}>
            All
          </PodcastPill>
          {podcastLibraries.map((library) => (
            <PodcastPill
              key={library.id}
              to={`/podcasts?library_id=${library.id}`}
              active={library.id === libraryID}
            >
              {library.name}
            </PodcastPill>
          ))}
        </div>
      )}

      {items.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
          {items.map((p) => (
            <Link
              key={p.id}
              to={`/podcasts/${encodeURIComponent(p.id)}`}
              className="group block"
            >
              <Card className="bg-surface hover:bg-surface-hover overflow-hidden border-0 p-0 transition-colors">
                <div className="bg-muted relative aspect-square w-full overflow-hidden">
                  {p.cover_url ? (
                    // eslint-disable-next-line jsx-a11y/img-redundant-alt
                    <img
                      src={p.cover_url}
                      alt={`${p.title} cover`}
                      loading="lazy"
                      className="size-full object-cover transition-transform group-hover:scale-105"
                    />
                  ) : (
                    <div className="text-muted-foreground flex size-full items-center justify-center">
                      <Mic className="size-10" />
                    </div>
                  )}
                </div>
                <div className="p-3">
                  <div className="line-clamp-2 text-sm font-medium">{p.title}</div>
                  {p.author && (
                    <div className="text-muted-foreground mt-0.5 line-clamp-1 text-xs">
                      {p.author}
                    </div>
                  )}
                </div>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

function PodcastPill({
  to,
  active,
  children,
}: {
  to: string;
  active: boolean;
  children: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className={
        active
          ? 'rounded-full border border-foreground bg-foreground px-3 py-1 text-xs font-medium text-background'
          : 'border-border bg-surface text-foreground hover:bg-surface-hover rounded-full border px-3 py-1 text-xs font-medium transition-colors'
      }
    >
      {children}
    </Link>
  );
}

function EmptyState() {
  return (
    <Card className="bg-surface space-y-3 p-8 text-center">
      <Rss className="text-muted-foreground mx-auto size-10" />
      <div className="text-sm">No podcasts yet.</div>
      <div className="text-muted-foreground text-xs">
        Admins add podcasts via <code>POST /api/v1/admin/podcasts</code> with a
        title, library_id, and an RSS <code>feed_url</code>. The feed refresher
        runs every 10 minutes and seeds new episodes automatically.
      </div>
    </Card>
  );
}
