import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useNavigate } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import {
  ArrowRight,
  BookHeadphones,
  Headphones,
  Home as HomeIcon,
  Layers,
  Library,
  ListChecks,
  Mic,
  Search,
  Settings,
  Smartphone,
  Sparkles,
  Users,
} from 'lucide-react';
import { api } from '@/api/client';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

// Command palette: opens on Cmd/Ctrl-K, lists every nav destination
// plus recent audiobooks fetched from the catalog. Substring-match
// scoring is inline — no external fuzzy lib — because the
// candidate list is small (~30 entries plus the recent books) and
// adding a 30 KB dep for cmd-K is silly.

type Cmd = {
  id: string;
  label: string;
  hint?: string;
  icon: ReactNode;
  perform: () => void;
};

// score returns a number in [0, ∞) measuring how well a candidate
// matches the query. 0 = no match. Higher = better.
//
// Algorithm: walk q across c lower-cased; bonus for prefix matches,
// word-boundary matches, and consecutive characters. Returns 0 if
// any char in q doesn't appear in c at or after the previous match.
function score(query: string, candidate: string): number {
  if (!query) return 1;
  const q = query.toLowerCase();
  const c = candidate.toLowerCase();
  if (q === c) return 1000;
  if (c.startsWith(q)) return 500;
  let s = 0;
  let lastIdx = -1;
  let consec = 0;
  for (let i = 0; i < q.length; i++) {
    const idx = c.indexOf(q[i], lastIdx + 1);
    if (idx < 0) return 0;
    if (idx === lastIdx + 1) {
      consec++;
      s += 5 + consec;
    } else {
      consec = 0;
      s += 1;
    }
    if (idx === 0 || c[idx - 1] === ' ') s += 3; // word-boundary bonus
    lastIdx = idx;
  }
  return s;
}

type Ctx = { open: () => void; close: () => void; isOpen: boolean };
const CommandCtx = createContext<Ctx | null>(null);

// useCommandPalette exposes open/close handles to other parts of
// the app (e.g. a header button that mirrors Cmd-K).
export function useCommandPalette() {
  const ctx = useContext(CommandCtx);
  if (!ctx) throw new Error('useCommandPalette must be used within CommandPaletteProvider');
  return ctx;
}

export function CommandPaletteProvider({ children }: { children: ReactNode }) {
  const [isOpen, setOpen] = useState(false);
  const open = useCallback(() => setOpen(true), []);
  const close = useCallback(() => setOpen(false), []);

  // Global Cmd-K / Ctrl-K to toggle. Ignored when the user is in an
  // input — the palette itself is an input, and we don't want to
  // double-toggle while typing.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setOpen((v) => !v);
      } else if (e.key === 'Escape') {
        setOpen(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <CommandCtx.Provider value={{ open, close, isOpen }}>
      {children}
      {isOpen && <Palette close={close} />}
    </CommandCtx.Provider>
  );
}

// Palette is the inner overlay. Mounted only when open so unmounting
// resets the query + selection. Listens for ↑/↓/Enter and clicks.
function Palette({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Recent books — top 20 by added_at desc. Cached for 60s; the
  // palette is meant to be fast-open, no spinner.
  const recents = useQuery({
    queryKey: ['cmdk-recent-books'],
    queryFn: () => api.listAudiobooks({ limit: 20, sort: 'added_at', order: 'desc' }),
    staleTime: 60_000,
  });

  const commands = useMemo<Cmd[]>(() => {
    const navCommands: Cmd[] = [
      { id: 'nav-home', label: 'Home', icon: <HomeIcon className="size-4" />, perform: () => navigate('/') },
      { id: 'nav-library', label: 'Library', icon: <Library className="size-4" />, perform: () => navigate('/library') },
      { id: 'nav-authors', label: 'Authors', icon: <Users className="size-4" />, perform: () => navigate('/authors') },
      { id: 'nav-series', label: 'Series', icon: <Layers className="size-4" />, perform: () => navigate('/series') },
      { id: 'nav-narrators', label: 'Narrators', icon: <Mic className="size-4" />, perform: () => navigate('/narrators') },
      { id: 'nav-collections', label: 'Collections', icon: <ListChecks className="size-4" />, perform: () => navigate('/collections') },
      { id: 'nav-smart-collections', label: 'Smart collections', icon: <Sparkles className="size-4" />, perform: () => navigate('/smart-collections') },
      { id: 'nav-podcasts', label: 'Podcasts', icon: <Headphones className="size-4" />, perform: () => navigate('/podcasts') },
      { id: 'nav-requests', label: 'My Requests', icon: <ListChecks className="size-4" />, perform: () => navigate('/me/requests') },
      { id: 'nav-apps', label: 'Apps', icon: <Smartphone className="size-4" />, perform: () => navigate('/apps') },
      { id: 'nav-admin', label: 'Admin', hint: 'requires admin access', icon: <Settings className="size-4" />, perform: () => navigate('/admin') },
    ];
    const bookCommands: Cmd[] = (recents.data?.items ?? []).map((b) => ({
      id: `book-${b.id}`,
      label: b.title,
      hint: b.authors?.join(', '),
      icon: <BookHeadphones className="size-4" />,
      perform: () => navigate(`/library/${encodeURIComponent(b.id)}`),
    }));
    return [...navCommands, ...bookCommands];
  }, [recents.data, navigate]);

  // Score + sort. Empty query keeps the natural nav-then-books order.
  const filtered = useMemo(() => {
    if (!query.trim()) return commands.slice(0, 20);
    const scored = commands
      .map((c) => ({ c, s: Math.max(score(query, c.label), score(query, c.hint ?? '') / 2) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s);
    return scored.slice(0, 20).map((x) => x.c);
  }, [commands, query]);

  // Reset selection whenever the filter changes.
  useEffect(() => {
    setSelectedIdx(0);
  }, [query]);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const perform = useCallback(
    (cmd: Cmd) => {
      close();
      cmd.perform();
    },
    [close],
  );

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelectedIdx((i) => Math.min(filtered.length - 1, i + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelectedIdx((i) => Math.max(0, i - 1));
    } else if (e.key === 'Enter' && filtered[selectedIdx]) {
      e.preventDefault();
      perform(filtered[selectedIdx]);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm pt-[15vh]"
      onClick={close}
    >
      <div
        className="bg-surface border-border w-full max-w-xl overflow-hidden rounded-xl border shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="border-border flex items-center gap-2 border-b px-4 py-3">
          <Search className="text-muted-foreground size-4" />
          <Input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Search books, libraries, or actions…"
            className="border-0 bg-transparent text-base shadow-none focus-visible:ring-0"
          />
          <kbd className="border-border text-muted-foreground hidden rounded border px-1.5 py-0.5 text-xs sm:inline-block">
            esc
          </kbd>
        </div>
        <div className="max-h-[60vh] overflow-y-auto">
          {filtered.length === 0 ? (
            <div className="text-muted-foreground px-4 py-6 text-center text-sm">No matches.</div>
          ) : (
            filtered.map((c, i) => (
              <button
                key={c.id}
                type="button"
                onClick={() => perform(c)}
                onMouseEnter={() => setSelectedIdx(i)}
                className={cn(
                  'flex w-full items-center gap-3 px-4 py-2.5 text-left text-sm',
                  i === selectedIdx ? 'bg-surface-hover' : 'hover:bg-surface-hover/60',
                )}
              >
                <span className="text-muted-foreground shrink-0">{c.icon}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{c.label}</span>
                  {c.hint && (
                    <span className="text-muted-foreground block truncate text-xs">{c.hint}</span>
                  )}
                </span>
                <ArrowRight className="text-muted-foreground/60 size-3 shrink-0" />
              </button>
            ))
          )}
        </div>
        <div className="border-border text-muted-foreground flex items-center justify-between border-t px-4 py-2 text-xs">
          <span>
            <kbd className="border-border rounded border px-1">↑</kbd>{' '}
            <kbd className="border-border rounded border px-1">↓</kbd> navigate
          </span>
          <span>
            <kbd className="border-border rounded border px-1">↵</kbd> open
          </span>
        </div>
      </div>
    </div>
  );
}
