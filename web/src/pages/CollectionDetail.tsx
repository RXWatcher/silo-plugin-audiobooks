import { useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';

export default function CollectionDetail() {
  const { id = '' } = useParams();
  const q = useQuery({
    queryKey: ['collection', id, 'items'],
    queryFn: () => api.listCollectionItems(id),
    enabled: !!id,
  });
  const items = q.data?.items ?? [];
  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Collection</h2>
      {items.length === 0 && <div className="text-muted-foreground">Empty.</div>}
      <div className="space-y-2">
        {items.map((it) => (
          <Card key={it.book_id} className="bg-surface flex items-center justify-between p-3">
            <div className="font-mono text-xs">{it.book_id}</div>
            <div className="text-muted-foreground text-xs">#{it.position}</div>
          </Card>
        ))}
      </div>
    </div>
  );
}
