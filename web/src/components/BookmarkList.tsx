import { useState } from 'react';
import { Pencil, Trash2, Check, X } from 'lucide-react';
import type { AudiobookChapter, Bookmark } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

// chapterAt finds the chapter that contains the given position, used
// for auto-titling bookmarks ("Chapter 3 — 12:34") when the user hasn't
// supplied a custom note. Linear scan because chapter lists are
// typically under 100 entries; not worth a binary search yet.
function chapterAt(chapters: AudiobookChapter[] | undefined, position: number): AudiobookChapter | undefined {
  if (!chapters || chapters.length === 0) return undefined;
  for (const c of chapters) {
    if (position >= c.start_seconds && position < c.end_seconds) return c;
  }
  return undefined;
}

export default function BookmarkList({
  bookmarks,
  chapters,
  onDelete,
  onSelect,
  onRename,
}: {
  bookmarks: Bookmark[];
  chapters?: AudiobookChapter[];
  onDelete?: (id: string) => void;
  onSelect?: (position: number) => void;
  onRename?: (id: string, note: string) => void;
}) {
  if (!bookmarks.length)
    return <div className="text-muted-foreground text-sm">No bookmarks yet.</div>;

  return (
    <div className="space-y-1">
      {bookmarks.map((b) => (
        <BookmarkRow
          key={b.id}
          bookmark={b}
          chapter={chapterAt(chapters, b.position_seconds)}
          onDelete={onDelete}
          onSelect={onSelect}
          onRename={onRename}
        />
      ))}
    </div>
  );
}

// BookmarkRow holds the inline rename state. Keeping it per-row
// means typing in one row doesn't re-render the rest of the list.
function BookmarkRow({
  bookmark: b,
  chapter,
  onDelete,
  onSelect,
  onRename,
}: {
  bookmark: Bookmark;
  chapter?: AudiobookChapter;
  onDelete?: (id: string) => void;
  onSelect?: (position: number) => void;
  onRename?: (id: string, note: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(b.note ?? '');

  // Auto-title: when no user note, render the chapter context
  // ("Chapter 3 — 12:34") so the row is never just a bare timestamp.
  const autoTitle = chapter ? `${chapter.title} — ${fmt(b.position_seconds)}` : null;

  const commit = () => {
    const trimmed = draft.trim();
    onRename?.(b.id, trimmed);
    setEditing(false);
  };

  return (
    <div className="hover:bg-surface-hover flex items-center gap-2 rounded-md px-3 py-2 text-sm">
      <button
        type="button"
        onClick={() => onSelect?.(b.position_seconds)}
        className="flex-1 min-w-0 text-left"
        disabled={editing}
      >
        <div className="text-muted-foreground tabular-nums text-xs">{fmt(b.position_seconds)}</div>
        {editing ? null : b.note ? (
          <div className="mt-1 truncate">{b.note}</div>
        ) : autoTitle ? (
          <div className="text-muted-foreground mt-1 truncate italic">{autoTitle}</div>
        ) : null}
      </button>
      {editing ? (
        <>
          <Input
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commit();
              if (e.key === 'Escape') {
                setEditing(false);
                setDraft(b.note ?? '');
              }
            }}
            placeholder={autoTitle ?? 'Bookmark note'}
            className="h-8 flex-1"
          />
          <Button size="icon" variant="ghost" onClick={commit} aria-label="Save">
            <Check className="size-4" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            onClick={() => {
              setEditing(false);
              setDraft(b.note ?? '');
            }}
            aria-label="Cancel"
          >
            <X className="size-4" />
          </Button>
        </>
      ) : (
        <>
          {onRename && (
            <Button
              size="icon"
              variant="ghost"
              onClick={() => setEditing(true)}
              aria-label="Rename bookmark"
            >
              <Pencil className="size-4" />
            </Button>
          )}
          {onDelete && (
            <Button
              size="icon"
              variant="ghost"
              onClick={() => onDelete(b.id)}
              aria-label="Delete bookmark"
            >
              <Trash2 className="size-4" />
            </Button>
          )}
        </>
      )}
    </div>
  );
}
