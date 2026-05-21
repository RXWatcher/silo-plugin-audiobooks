import { Link } from 'react-router';
import { useInfiniteQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import InfiniteFooter from '@/components/InfiniteFooter';
import type { PageEnvelope, AuthorSummary } from '@/api/types';

const PAGE_SIZE = 100;

export default function Authors() {
  // Infinite scroll instead of a hardcoded limit:200. A growing library
  // silently truncated under the old fixed cap; cursor-based pagination
  // surfaces "Load more" so the operator knows there's more to see.
  const q = useInfiniteQuery({
    queryKey: ['browse', 'authors'],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api.browseAuthors({ limit: PAGE_SIZE, cursor: pageParam as string | undefined }),
    getNextPageParam: (last: PageEnvelope<AuthorSummary>) => last.next_cursor || undefined,
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
      <h2 className="text-2xl font-semibold">Authors</h2>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
        {items.map((a) => (
          <Link key={a.id} to={`/authors/${encodeURIComponent(a.id)}`}>
            <Card className="bg-surface hover:bg-surface-hover px-4 py-3 transition-colors">
              <div className="font-medium">{a.name}</div>
              {a.book_count !== undefined && (
                <div className="text-muted-foreground text-xs">{a.book_count} books</div>
              )}
            </Card>
          </Link>
        ))}
      </div>
      <InfiniteFooter
        hasNextPage={q.hasNextPage}
        isFetchingNextPage={q.isFetchingNextPage}
        fetchNextPage={() => q.fetchNextPage()}
        label="authors"
      />
    </div>
  );
}
