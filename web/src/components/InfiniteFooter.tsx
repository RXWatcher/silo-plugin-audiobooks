import { useEffect, useRef } from 'react';

type Props = {
  hasNextPage: boolean | undefined;
  isFetchingNextPage: boolean;
  fetchNextPage: () => void;
  label: string;
};

// InfiniteFooter combines an IntersectionObserver sentinel (auto-fetches when
// scrolled near the end) with a visible button (manual fallback for keyboard
// users and anyone whose IO is disabled — e.g. some accessibility tools).
// rootMargin: 600px starts fetching about a screen ahead of the user so they
// rarely see the "Loading…" state.
export default function InfiniteFooter({
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
  label,
}: Props) {
  const sentinelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!hasNextPage) return;
    const node = sentinelRef.current;
    if (!node) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) {
          fetchNextPage();
        }
      },
      { rootMargin: '600px' },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  if (!hasNextPage) return null;
  return (
    <div ref={sentinelRef} className="flex justify-center pt-4">
      <button
        type="button"
        onClick={() => fetchNextPage()}
        disabled={isFetchingNextPage}
        className="rounded-md border border-border bg-surface px-4 py-2 text-sm font-medium hover:bg-surface-hover disabled:opacity-50"
      >
        {isFetchingNextPage ? 'Loading…' : `Load more ${label}`}
      </button>
    </div>
  );
}
