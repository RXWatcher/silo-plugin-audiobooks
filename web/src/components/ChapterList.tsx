import type { AudiobookChapter } from '@/api/types';

function fmt(t: number): string {
  if (!Number.isFinite(t) || t < 0) return '0:00';
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = Math.floor(t % 60);
  if (h) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  return `${m}:${String(s).padStart(2, '0')}`;
}

export default function ChapterList({
  chapters,
  activePosition,
  onSelect,
}: {
  chapters: AudiobookChapter[];
  activePosition?: number;
  onSelect?: (position: number) => void;
}) {
  if (!chapters.length) return null;
  return (
    <div className="space-y-1">
      {chapters.map((c, i) => (
        <button
          key={i}
          type="button"
          onClick={() => onSelect?.(c.start_seconds)}
          className={`flex w-full items-center justify-between rounded-md px-3 py-2 text-left text-sm hover:bg-surface-hover ${
            activePosition != null &&
            activePosition >= c.start_seconds &&
            activePosition < c.end_seconds
              ? 'bg-primary/10 text-primary'
              : ''
          }`}
        >
          <div className="flex-1 truncate">
            <span className="text-muted-foreground mr-2 text-xs tabular-nums">
              {String(i + 1).padStart(2, '0')}
            </span>
            {c.title}
          </div>
          <div className="text-muted-foreground tabular-nums text-xs">
            {fmt(c.start_seconds)}
          </div>
        </button>
      ))}
    </div>
  );
}
