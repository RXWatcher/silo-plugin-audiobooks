import { useQueries, useQuery } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import { api } from '@/api/client';
import AudiobookGrid from '@/components/AudiobookGrid';
import SearchBar from '@/components/SearchBar';
import { isAdmin } from '@/lib/identity';
import type { AudiobookDetail, AudiobookSummary } from '@/api/types';

export default function Home() {
  const [params] = useSearchParams();
  const q = params.get('q') ?? '';

  if (q) {
    return <SearchResults q={q} />;
  }
  return <Shelves />;
}

function Shelves() {
  const [params] = useSearchParams();
  const admin = isAdmin();
  const libraryID = Number(params.get('library_id') || 0) || undefined;
  const libraries = useQuery({
    queryKey: ['libraries'],
    queryFn: () => api.listLibraries(),
  });

  // Continue-listening shelf — distinct from the cache the grid cards read.
  // The cards use ['progress','recent'] (limit 50, full list) so they can
  // resolve any book's progress without refetching per-card. This key is
  // narrower (12, library-scoped) and feeds only the Continue Listening row.
  const progress = useQuery({
    queryKey: ['progress', 'continue-listening', libraryID],
    queryFn: () => api.listMyProgress(12),
  });

  const recent = useQuery({
    queryKey: ['audiobooks', 'recent', libraryID],
    queryFn: () => api.listAudiobooks({ sort: 'added', order: 'desc', limit: 24, library_id: libraryID }),
  });

  return (
    <div className="space-y-10">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <h2 className="text-2xl font-semibold tracking-tight">Audiobooks</h2>
        <div className="w-full max-w-md">
          <SearchBar />
        </div>
      </div>

      {libraries.data && libraries.data.items.length > 1 && (
        <div className="flex flex-wrap gap-2">
          <LibraryPill to="/" active={!libraryID}>
            All
          </LibraryPill>
          {libraries.data.items.map((library) => (
            <LibraryPill
              key={library.id}
              to={`/?library_id=${library.id}`}
              active={library.id === libraryID}
            >
              {library.name}
            </LibraryPill>
          ))}
        </div>
      )}

      <ContinueListeningShelf progressBookIds={progress.data?.items.map((p) => p.book_id) ?? []} />


      <section>
        <h3 className="mb-3 text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Recently added
        </h3>
        <AudiobookGrid
          items={recent.data?.items ?? []}
          loading={recent.isLoading}
          empty={<EmptyLibraryState isAdmin={admin} />}
        />
      </section>
    </div>
  );
}

// ContinueListeningShelf fans out N parallel detail fetches for the user's
// most-recent in-progress books, then renders them as a cover grid. The
// /me/progress endpoint only carries (book_id, progress_pct) pairs — book
// metadata lives in the backend plugin, so the join has to happen client-
// side until/unless the portal grows a "progress with book metadata" route.
function ContinueListeningShelf({ progressBookIds }: { progressBookIds: string[] }) {
  const queries = useQueries({
    queries: progressBookIds.map((id) => ({
      queryKey: ['audiobook', id],
      queryFn: () => api.getAudiobook(id),
      // The detail RPC is cheap server-side (the portal caches catalog), but
      // many cards x reload still adds up. 5 min keeps it warm without
      // blocking fresh progress reads.
      staleTime: 5 * 60 * 1000,
    })),
  });
  if (!progressBookIds.length) return null;
  // AudiobookGrid only needs the summary-shaped subset of detail — drop the
  // detail-only files/chapters fields before handing off so the type matches.
  const items: AudiobookSummary[] = queries
    .map((q) => q.data?.audiobook as AudiobookDetail | undefined)
    .filter((b): b is AudiobookDetail => Boolean(b))
    .map((b) => b as AudiobookSummary);
  // Don't flash an empty shelf during the initial fetch — wait until at
  // least one detail comes back. If none do (all errored), hide the section.
  const loading = queries.some((q) => q.isLoading);
  if (!items.length && !loading) return null;
  return (
    <section>
      <h3 className="mb-3 text-sm font-medium uppercase tracking-wide text-muted-foreground">
        Continue listening
      </h3>
      <AudiobookGrid items={items} loading={loading} empty={null} />
    </section>
  );
}

function EmptyLibraryState({ isAdmin }: { isAdmin: boolean }) {
  return (
    <div className="rounded-lg border border-dashed border-border bg-surface/40 p-8 text-center">
      <h3 className="text-base font-semibold text-foreground">No audiobooks are available yet</h3>
      <p className="mx-auto mt-2 max-w-xl text-sm text-muted-foreground">
        The portal is running, but no reachable audiobook library is currently selected.
      </p>
      {isAdmin ? (
        <div className="mt-4 flex flex-wrap justify-center gap-2">
          <Link
            to="/admin"
            className="inline-flex min-h-9 items-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground"
          >
            Configure backend
          </Link>
          <Link
            to="/admin"
            className="inline-flex min-h-9 items-center rounded-md border border-border px-3 text-sm font-medium"
          >
            Review requests
          </Link>
        </div>
      ) : (
        <p className="mt-4 text-xs text-muted-foreground">
          Ask an administrator to connect an audiobook backend or run a library scan.
        </p>
      )}
    </div>
  );
}

function SearchResults({ q }: { q: string }) {
  const [params] = useSearchParams();
  const libraryID = Number(params.get('library_id') || 0) || undefined;
  const results = useQuery({
    queryKey: ['audiobooks', 'search', q, libraryID],
    queryFn: () => api.searchAudiobooks(q, libraryID),
  });

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-4">
        <h2 className="text-xl font-semibold tracking-tight">Results for "{q}"</h2>
        <div className="w-full max-w-md">
          <SearchBar />
        </div>
      </div>
      <AudiobookGrid
        items={results.data?.items ?? []}
        loading={results.isLoading}
        empty={`No results for "${q}". Don't see it? Try requesting it from any book detail page.`}
      />
    </div>
  );
}

function LibraryPill({
  to,
  active,
  children,
}: {
  to: string;
  active?: boolean;
  children: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className={`rounded-full border px-3 py-1 text-sm transition-colors ${
        active
          ? 'border-primary bg-primary/10 text-primary'
          : 'border-border bg-background text-muted-foreground hover:bg-surface-hover hover:text-foreground'
      }`}
    >
      {children}
    </Link>
  );
}
