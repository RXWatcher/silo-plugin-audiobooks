import { useMemo, useState } from 'react';

// Atmosphere mode — soft animated gradient backdrop seeded from the
// active book's title so each book gets its own colour palette. The
// overlay is non-interactive (pointer-events: none) and sits behind
// the page content; users opt in via localStorage.
//
// No video, no audio loop — readest renders an ambient video in
// Atmosphere mode but the licensing of stock-footage backgrounds is
// thorny for an OSS plugin. The colour-flow version delivers most of
// the calming-presence effect without the licensing tail.

const STORAGE_KEY = 'audiobooks.atmosphere.enabled';

export function useAtmosphereEnabled(): [boolean, (v: boolean) => void] {
  const [enabled, setEnabled] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem(STORAGE_KEY) === 'true';
    } catch {
      return false;
    }
  });
  const set = (v: boolean) => {
    setEnabled(v);
    try {
      window.localStorage.setItem(STORAGE_KEY, v ? 'true' : 'false');
    } catch {
      /* private-mode storage, ignore */
    }
  };
  return [enabled, set];
}

// hashString folds a string into a 32-bit unsigned int via FNV-1a.
// We use it to seed the per-book palette so the same book always
// gets the same atmosphere even across sessions.
function hashString(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// hslColor builds a CSS HSL string from a hash + offset, with a
// fixed saturation + lightness picked for low-contrast backgrounds
// that don't fight the text on top.
function hslColor(seed: number, offset: number): string {
  const hue = (seed + offset) % 360;
  return `hsl(${hue}, 45%, 30%)`;
}

export function AtmosphereOverlay({ seed }: { seed: string }) {
  const palette = useMemo(() => {
    const h = hashString(seed || 'default');
    return {
      a: hslColor(h, 0),
      b: hslColor(h, 120),
      c: hslColor(h, 240),
    };
  }, [seed]);

  // 60-second loop — slow enough that the motion isn't noticed
  // consciously, fast enough that a long session shows variation.
  // The keyframes are computed inline rather than via a stylesheet
  // so each unique palette doesn't leak a new CSS rule.
  return (
    <div
      aria-hidden
      className="fixed inset-0 -z-10 overflow-hidden"
      style={{ pointerEvents: 'none' }}
    >
      <div
        className="absolute -inset-[20%] opacity-30 blur-3xl"
        style={{
          background: `radial-gradient(circle at 20% 30%, ${palette.a}, transparent 40%),
                       radial-gradient(circle at 80% 70%, ${palette.b}, transparent 40%),
                       radial-gradient(circle at 50% 50%, ${palette.c}, transparent 50%)`,
          animation: 'atmosphere-drift 60s ease-in-out infinite',
        }}
      />
      <style>{`
        @keyframes atmosphere-drift {
          0%   { transform: translate(0, 0) scale(1);   }
          25%  { transform: translate(2%, -1%) scale(1.05); }
          50%  { transform: translate(-1%, 2%) scale(0.98); }
          75%  { transform: translate(-2%, -2%) scale(1.03); }
          100% { transform: translate(0, 0) scale(1);   }
        }
      `}</style>
    </div>
  );
}

// AtmosphereToggle is the user-facing switch. Renders nothing when
// not present; lives in the player or settings page.
export function AtmosphereToggle() {
  const [enabled, setEnabled] = useAtmosphereEnabled();
  return (
    <button
      type="button"
      onClick={() => setEnabled(!enabled)}
      className="text-muted-foreground hover:bg-surface-hover hover:text-foreground inline-flex min-h-9 items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors"
      title={enabled ? 'Disable atmosphere mode' : 'Enable atmosphere mode'}
    >
      <span
        className={enabled ? 'bg-primary size-2 rounded-full' : 'bg-muted-foreground/40 size-2 rounded-full'}
        aria-hidden
      />
      Atmosphere
    </button>
  );
}
