import { Outlet, NavLink, useLocation } from 'react-router';
import {
  ArrowLeft,
  BookHeadphones,
  Headphones,
  Home as HomeIcon,
  Layers,
  Library,
  Search,
  Smartphone,
  ListChecks,
  Users,
  Mic,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { CommandPaletteProvider, useCommandPalette } from '@/components/CommandPalette';
import { ShortcutHelpProvider } from '@/components/ShortcutHelp';
import { AtmosphereOverlay, useAtmosphereEnabled } from '@/components/AtmosphereOverlay';
import { usePlayback } from '@/player/PlaybackProvider';

export default function Layout() {
  return (
    <ShortcutHelpProvider>
      <CommandPaletteProvider>
        <LayoutInner />
      </CommandPaletteProvider>
    </ShortcutHelpProvider>
  );
}

function LayoutInner() {
  const loc = useLocation();
  const isAdminRoute = loc.pathname.startsWith('/admin');
  const backToContinuumHref = isAdminRoute ? '/admin/plugins' : '/';
  const backToContinuumTitle = isAdminRoute ? 'Back to Continuum plugins' : 'Back to Continuum';

  return (
    <div className="bg-background relative min-h-[100dvh] overflow-x-hidden">
      <Atmosphere />
      <div className="from-primary/6 pointer-events-none fixed inset-x-0 top-0 z-0 h-40 bg-gradient-to-b to-transparent blur-3xl" />

      <header className="glass-dark border-border/70 sticky top-0 z-30 mx-3 mt-3 flex items-center justify-between rounded-2xl border px-4 py-3 sm:mx-6 lg:mx-8">
        <div className="flex items-center gap-3">
          <a
            href={backToContinuumHref}
            className="text-muted-foreground hover:bg-surface-hover hover:text-foreground inline-flex min-h-9 min-w-9 items-center justify-center gap-1.5 rounded-lg px-2 py-1.5 text-xs font-medium transition-colors"
            title={backToContinuumTitle}
          >
            <ArrowLeft className="size-4" />
            <span className="hidden sm:inline">Continuum</span>
          </a>
          <span className="text-border/60" aria-hidden>
            /
          </span>
          <h1 className="flex items-center gap-2 text-base font-semibold tracking-tight">
            <BookHeadphones className="size-5" />
            Audiobooks
          </h1>
        </div>
        <nav className="flex items-center gap-1">
          <CmdKButton />
          <NavItem to="/" icon={<HomeIcon className="size-4" />} label="Home" exact />
          <NavItem to="/library" icon={<Library className="size-4" />} label="Library" />
          <NavItem to="/authors" icon={<Users className="size-4" />} label="Authors" />
          <NavItem to="/series" icon={<Layers className="size-4" />} label="Series" />
          <NavItem to="/narrators" icon={<Mic className="size-4" />} label="Narrators" />
          <NavItem to="/collections" icon={<ListChecks className="size-4" />} label="Collections" />
          <NavItem to="/podcasts" icon={<Headphones className="size-4" />} label="Podcasts" />
          <NavItem to="/me/requests" icon={<ListChecks className="size-4" />} label="My Requests" />
          <NavItem to="/apps" icon={<Smartphone className="size-4" />} label="Apps" />
          {/*
            No "Admin" tab here. The user portal is strictly user-facing;
            admins reach the plugin's admin UI via the continuum host
            sidebar (Apps → Books → Audiobooks → [admin]). Mixing the two
            surfaces in this nav blurred the audience.
          */}
        </nav>
      </header>

      <main
        id="main-content"
        className="relative z-10 mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8"
      >
        <Outlet />
      </main>
    </div>
  );
}

function Atmosphere() {
  const [enabled] = useAtmosphereEnabled();
  const playback = usePlayback();
  if (!enabled || !playback.audiobook) return null;
  return <AtmosphereOverlay seed={playback.audiobook.title} />;
}

function CmdKButton() {
  const { open } = useCommandPalette();
  return (
    <button
      type="button"
      onClick={open}
      title="Search (Cmd-K)"
      className="text-muted-foreground hover:bg-surface-hover hover:text-foreground inline-flex min-h-9 items-center justify-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors"
    >
      <Search className="size-4" />
      <kbd className="border-border hidden rounded border px-1.5 py-0.5 text-xs sm:inline">⌘K</kbd>
    </button>
  );
}

function NavItem({
  to,
  icon,
  label,
  exact,
}: {
  to: string;
  icon: React.ReactNode;
  label: string;
  exact?: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={exact}
      className={({ isActive }) =>
        cn(
          'inline-flex min-h-9 items-center justify-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
          isActive
            ? 'bg-surface text-foreground'
            : 'text-muted-foreground hover:bg-surface-hover hover:text-foreground',
        )
      }
    >
      {icon}
      <span className="hidden sm:inline">{label}</span>
    </NavLink>
  );
}
