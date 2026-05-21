import { Link } from 'react-router';
import { useInfiniteQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import InfiniteFooter from '@/components/InfiniteFooter';
import type { PageEnvelope, NarratorSummary } from '@/api/types';

const PAGE_SIZE = 100;

export default function Narrators() {
  const q = useInfiniteQuery({
    queryKey: ['browse', 'narrators'],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api.browseNarrators({ limit: PAGE_SIZE, cursor: pageParam as string | undefined }),
    getNextPageParam: (last: PageEnvelope<NarratorSummary>) => last.next_cursor || undefined,
  });
  if (q.isLoading)
    return (
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
        {Array.from({ length: 16 }).map((_, i) => (
          <Skeleton key={i} className="h-16 w-full" />
        ))}
      </div>
    );
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Narrators</h2>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
        {items.map((n) => (
          <Link key={n.id} to={`/narrators/${encodeURIComponent(n.id)}`}>
            <Card className="bg-surface hover:bg-surface-hover px-4 py-3 transition-colors">
              <div className="font-medium">{n.name}</div>
              {n.book_count !== undefined && (
                <div className="text-muted-foreground text-xs">{n.book_count} books</div>
              )}
            </Card>
          </Link>
        ))}
      </div>
      <InfiniteFooter
        hasNextPage={q.hasNextPage}
        isFetchingNextPage={q.isFetchingNextPage}
        fetchNextPage={() => q.fetchNextPage()}
        label="narrators"
      />
    </div>
  );
}
