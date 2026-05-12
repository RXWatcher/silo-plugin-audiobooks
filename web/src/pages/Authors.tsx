import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';

export default function Authors() {
  const q = useQuery({
    queryKey: ['browse', 'authors'],
    queryFn: () => api.browseAuthors({ limit: 200 }),
  });
  if (q.isLoading)
    return (
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
        {Array.from({ length: 16 }).map((_, i) => (
          <Skeleton key={i} className="h-16 w-full" />
        ))}
      </div>
    );
  const items = q.data?.items ?? [];
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
    </div>
  );
}
