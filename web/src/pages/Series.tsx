import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';

export default function Series() {
  const q = useQuery({ queryKey: ['browse', 'series'], queryFn: () => api.browseSeries({ limit: 100 }) });
  if (q.isLoading)
    return (
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4">
        {Array.from({ length: 12 }).map((_, i) => (
          <Skeleton key={i} className="h-24 w-full" />
        ))}
      </div>
    );
  const items = q.data?.items ?? [];
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
    </div>
  );
}
