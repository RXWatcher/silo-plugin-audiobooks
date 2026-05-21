import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { useSearchParams } from 'react-router';
import { api } from '@/api/client';
import AudiobookGrid from '@/components/AudiobookGrid';
import InfiniteFooter from '@/components/InfiniteFooter';
import type { PageEnvelope, AudiobookSummary } from '@/api/types';

const PAGE_SIZE = 60;

// Library is the flat catalog view — every audiobook across enabled libraries,
// sorted by recency. Facet pages (Authors/Series/Narrators) are useful when
// the user knows what they're looking for; Library is for "show me everything
// I can listen to" plus library-scoped browsing via ?library_id=.
export default function Library() {
  const [params] = useSearchParams();
  const libraryID = Number(params.get('library_id') || 0) || undefined;
  const libraries = useQuery({
    queryKey: ['libraries'],
    queryFn: () => api.listLibraries(),
  });
  const currentLibrary = libraries.data?.items.find((l) => l.id === libraryID);

  const q = useInfiniteQuery({
    queryKey: ['library', 'all', libraryID],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api.listAudiobooks({
        limit: PAGE_SIZE,
        sort: 'added',
        order: 'desc',
        cursor: pageParam as string | undefined,
        library_id: libraryID,
      }),
    getNextPageParam: (last: PageEnvelope<AudiobookSummary>) => last.next_cursor || undefined,
  });

  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  const total = q.data?.pages[0]?.total;

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-baseline gap-3">
        <h2 className="text-2xl font-semibold">Library</h2>
        {currentLibrary ? (
          <span className="text-muted-foreground text-sm">{currentLibrary.name}</span>
        ) : null}
        {typeof total === 'number' ? (
          <span className="text-muted-foreground text-xs">
            {total.toLocaleString()} {total === 1 ? 'audiobook' : 'audiobooks'}
          </span>
        ) : null}
      </header>
      <AudiobookGrid
        items={items}
        loading={q.isLoading}
        empty="No audiobooks yet — check your backend configuration in admin."
      />
      <InfiniteFooter
        hasNextPage={q.hasNextPage}
        isFetchingNextPage={q.isFetchingNextPage}
        fetchNextPage={() => q.fetchNextPage()}
        label="audiobooks"
      />
    </div>
  );
}
