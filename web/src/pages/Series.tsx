import { Link } from 'react-router';
import { useInfiniteQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import InfiniteFooter from '@/components/InfiniteFooter';
import type { PageEnvelope, SeriesSummary } from '@/api/types';

const PAGE_SIZE = 100;

export default function Series() {
  const q = useInfiniteQuery({
    queryKey: ['browse', 'series'],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api.browseSeries({ limit: PAGE_SIZE, cursor: pageParam as string | undefined }),
    getNextPageParam: (last: PageEnvelope<SeriesSummary>) => last.next_cursor || undefined,
  });
  if (q.isLoading)
    return (
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4">
        {Array.from({ length: 12 }).map((_, i) => (
          <Skeleton key={i} className="h-24 w-full" />
        ))}
      </div>
    );
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Series</h2>
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4">
        {items.map((s) => (
          <Link key={s.id} to={`/series/${encodeURIComponent(s.id)}`}>
            <Card className="bg-surface hover:bg-surface-hover p-4 transition-colors">
              <div className="font-medium">{s.name}</div>
              {s.book_count !== undefined && (
                <div className="text-muted-foreground text-xs">{s.book_count} books</div>
              )}
            </Card>
          </Link>
        ))}
      </div>
      <InfiniteFooter
        hasNextPage={q.hasNextPage}
        isFetchingNextPage={q.isFetchingNextPage}
        fetchNextPage={() => q.fetchNextPage()}
        label="series"
      />
    </div>
  );
}
