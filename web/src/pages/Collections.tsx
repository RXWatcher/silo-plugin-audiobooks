import { useState } from 'react';
import { Link } from 'react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Pin, Globe } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';

export default function Collections() {
  const qc = useQueryClient();
  const mine = useQuery({ queryKey: ['collections', 'mine'], queryFn: () => api.listMyCollections() });
  const pub = useQuery({ queryKey: ['collections', 'public'], queryFn: () => api.listPublicCollections() });

  const [name, setName] = useState('');
  const create = useMutation({
    mutationFn: () => api.createCollection({ name }),
    onSuccess: () => {
      setName('');
      toast.success('Created');
      qc.invalidateQueries({ queryKey: ['collections', 'mine'] });
    },
    onError: (e) => toast.error(`${e}`),
  });

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <h2 className="text-2xl font-semibold">My collections</h2>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!name.trim()) return;
            create.mutate();
          }}
          className="flex gap-2"
        >
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="New collection name" />
          <Button type="submit" disabled={create.isPending}>
            Create
          </Button>
        </form>
        <CollectionList items={mine.data?.items ?? []} loading={mine.isLoading} />
      </section>

      <section className="space-y-3">
        <h2 className="text-2xl font-semibold">Public collections</h2>
        <CollectionList items={pub.data?.items ?? []} loading={pub.isLoading} />
      </section>
    </div>
  );
}

function CollectionList({ items, loading }: { items: Array<any>; loading?: boolean }) {
  if (loading) return <div className="text-muted-foreground">Loading...</div>;
  if (!items.length) return <div className="text-muted-foreground">None.</div>;
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {items.map((c) => (
        <Link key={c.id} to={`/collections/${encodeURIComponent(c.id)}`}>
          <Card className="bg-surface hover:bg-surface-hover flex items-center justify-between p-4 transition-colors">
            <div>
              <div className="font-medium">{c.name}</div>
              <div className="text-muted-foreground mt-1 flex items-center gap-2 text-xs">
                {c.is_pinned && <Pin className="size-3" />}
                {c.is_public && <Globe className="size-3" />}
                <span>Created {new Date(c.created_at).toLocaleDateString()}</span>
              </div>
            </div>
          </Card>
        </Link>
      ))}
    </div>
  );
}
