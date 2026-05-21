import { useState } from 'react';
import { Link } from 'react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Plus, Sparkles, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import type { SmartCollection, SmartCollectionQuery } from '@/api/types';

// Smart Collections list page. Each card links to the detail page
// where the user edits the query + browses matching items.
//
// "Create" button mints an empty-rule collection so the user lands
// on the builder rather than a blank-name modal — fewer clicks
// from "I want a new shelf" to "I'm picking rules."

const emptyQuery: SmartCollectionQuery = {
  match: 'all',
  groups: [],
  sort: { field: 'added_at', order: 'desc' },
};

export default function SmartCollections() {
  const qc = useQueryClient();
  const collections = useQuery({
    queryKey: ['smart-collections'],
    queryFn: () => api.listSmartCollections(),
  });

  const createMutation = useMutation({
    mutationFn: () =>
      api.createSmartCollection({
        name: 'New smart collection',
        query_def: emptyQuery,
      }),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ['smart-collections'] });
      // Soft-navigate by setting window.location — the SPA router
      // picks it up. Cleaner than threading a navigate() through
      // every mutation handler.
      window.location.hash = `#/smart-collections/${created.id}`;
    },
    onError: (err) => toast.error(`Create failed: ${err}`),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteSmartCollection(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['smart-collections'] }),
  });

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Smart collections</h2>
          <p className="text-muted-foreground text-sm">
            Rule-based shelves — anything matching your filters appears here.
          </p>
        </div>
        <Button onClick={() => createMutation.mutate()} disabled={createMutation.isPending}>
          <Plus className="size-4" />
          <span className="ml-1">New</span>
        </Button>
      </header>

      {collections.isLoading ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      ) : (collections.data?.items ?? []).length === 0 ? (
        <EmptyState onCreate={() => createMutation.mutate()} />
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {collections.data!.items.map((c) => (
            <CollectionCard
              key={c.id}
              collection={c}
              onDelete={() => deleteMutation.mutate(c.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function CollectionCard({
  collection,
  onDelete,
}: {
  collection: SmartCollection;
  onDelete: () => void;
}) {
  const ruleCount = collection.queryDef?.groups?.reduce((n, g) => n + g.rules.length, 0) ?? 0;
  return (
    <Card className="bg-surface hover:bg-surface-hover relative p-4 transition-colors">
      <Link
        to={`/smart-collections/${encodeURIComponent(collection.id)}`}
        className="block"
      >
        <div className="flex items-center gap-2">
          <Sparkles className="text-primary size-4" />
          <h3 className="truncate text-base font-medium">{collection.name}</h3>
        </div>
        {collection.description && (
          <p className="text-muted-foreground mt-2 line-clamp-2 text-xs">
            {collection.description}
          </p>
        )}
        <div className="text-muted-foreground mt-3 text-xs">
          {ruleCount} {ruleCount === 1 ? 'rule' : 'rules'}
          {collection.isPinned ? ' · pinned' : ''}
          {collection.isPublic ? ' · public' : ''}
        </div>
      </Link>
      <DeleteButton onClick={onDelete} />
    </Card>
  );
}

function DeleteButton({ onClick }: { onClick: () => void }) {
  const [confirming, setConfirming] = useState(false);
  return (
    <Button
      size="icon"
      variant="ghost"
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        if (confirming) {
          onClick();
          setConfirming(false);
        } else {
          setConfirming(true);
          setTimeout(() => setConfirming(false), 3000);
        }
      }}
      className="absolute right-2 top-2 size-8 opacity-0 transition-opacity group-hover:opacity-100 hover:opacity-100"
      title={confirming ? 'Click again to confirm' : 'Delete'}
    >
      <Trash2 className={confirming ? 'text-destructive size-4' : 'size-4'} />
    </Button>
  );
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <Card className="bg-surface flex flex-col items-center gap-3 p-10 text-center">
      <Sparkles className="text-muted-foreground size-10" />
      <h3 className="text-lg font-medium">No smart collections yet</h3>
      <p className="text-muted-foreground max-w-sm text-sm">
        Smart collections are rule-based shelves: instead of manually adding
        books, you define filters (genre, author, rating, year, listening
        status) and the shelf populates itself.
      </p>
      <Button onClick={onCreate} className="mt-2">
        <Plus className="size-4" />
        <span className="ml-1">Create your first</span>
      </Button>
    </Card>
  );
}
