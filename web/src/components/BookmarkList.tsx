import { Trash2 } from 'lucide-react';
import type { Bookmark } from '@/api/types';
import { Button } from '@/components/ui/button';

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

export default function BookmarkList({
  bookmarks,
  onDelete,
  onSelect,
}: {
  bookmarks: Bookmark[];
  onDelete?: (id: string) => void;
  onSelect?: (position: number) => void;
}) {
  if (!bookmarks.length) return <div className="text-muted-foreground text-sm">No bookmarks yet.</div>;
  return (
    <div className="space-y-1">
      {bookmarks.map((b) => (
        <div
          key={b.id}
          className="hover:bg-surface-hover flex items-center justify-between rounded-md px-3 py-2 text-sm"
        >
          <button
            type="button"
            onClick={() => onSelect?.(b.position_seconds)}
            className="flex-1 text-left"
          >
            <div className="text-muted-foreground tabular-nums text-xs">{fmt(b.position_seconds)}</div>
            {b.note && <div className="mt-1">{b.note}</div>}
          </button>
          {onDelete && (
            <Button size="icon" variant="ghost" onClick={() => onDelete(b.id)}>
              <Trash2 className="size-4" />
            </Button>
          )}
        </div>
      ))}
    </div>
  );
}
