import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { CheckCircle2, Headphones } from 'lucide-react';
import type { AudiobookSummary } from '@/api/types';
import { Card } from '@/components/ui/card';
import { api } from '@/api/client';

export default function AudiobookCard({ book }: { book: AudiobookSummary }) {
  // Pull the user's progress for this book from the shared per-user progress
  // cache. All grid cards share this single ['progress','recent'] query so a
  // grid of N books triggers exactly one progress request, not N. Other pages
  // (e.g. Continue Listening on Home) use a different key by design — they
  // want library-scoped slices and shouldn't invalidate this cross-grid view.
  const progress = useQuery({
    queryKey: ['progress', 'recent'],
    queryFn: () => api.listMyProgress(50),
    staleTime: 60 * 1000,
  });
  const entry = progress.data?.items.find((p) => p.book_id === book.id);
  const isFinished = entry?.is_finished ?? (entry?.progress_pct ?? 0) >= 0.99;
  const pct = Math.min(100, Math.max(0, Math.round((entry?.progress_pct ?? 0) * 100)));

  return (
    <Link to={`/audiobook/${encodeURIComponent(book.id)}`} className="group block">
      <Card className="bg-surface hover:bg-surface-hover overflow-hidden border-0 p-0 transition-colors">
        <div className="bg-muted relative aspect-[2/3] w-full overflow-hidden">
          {book.cover_url ? (
            <img
              src={book.cover_url}
              alt={book.title}
              loading="lazy"
              className="size-full object-cover transition-transform group-hover:scale-105"
            />
          ) : (
            <div className="text-muted-foreground flex size-full items-center justify-center">
              <Headphones className="size-10" />
            </div>
          )}
          {/*
            A "Finished" badge gives quick visual confirmation when scanning a
            shelf for what's left to listen to. In-progress shows a slim bar
            along the bottom edge of the cover so users can spot how far in
            they are without opening the detail page.
          */}
          {isFinished && (
            <div className="absolute right-2 top-2 flex items-center gap-1 rounded-full bg-primary/90 px-2 py-0.5 text-xs font-medium text-primary-foreground backdrop-blur-sm">
              <CheckCircle2 className="size-3" /> Finished
            </div>
          )}
          {!isFinished && entry && pct > 0 && (
            <div className="absolute inset-x-0 bottom-0 h-1 bg-background/40">
              <div className="h-full bg-primary" style={{ width: `${pct}%` }} />
            </div>
          )}
        </div>
        <div className="p-3">
          <div className="line-clamp-2 text-sm font-medium leading-snug">{book.title}</div>
          {book.authors && book.authors.length > 0 && (
            <div className="text-muted-foreground mt-1 line-clamp-1 text-xs">
              {book.authors.join(', ')}
            </div>
          )}
        </div>
      </Card>
    </Link>
  );
}
