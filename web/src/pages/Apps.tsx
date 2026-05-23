import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Apple, Smartphone } from 'lucide-react';

import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';

export default function Apps() {
  const absServerUrl = `${window.location.origin}${window.location.pathname.replace(/\/?$/, '')}`;
  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-semibold">Audiobookshelf mobile app</h2>
      <Card className="bg-surface space-y-4 p-6">
        <div className="flex items-center gap-3 text-sm">
          <Smartphone className="size-5" />
          <span>
            Listen on iOS or Android with the official Audiobookshelf app. Add this server URL in the app:
          </span>
        </div>
        <div className="bg-background overflow-auto rounded-md border p-3 font-mono text-xs">
          {absServerUrl}
        </div>
        <ol className="text-muted-foreground list-decimal space-y-1 pl-5 text-sm">
          <li>Install Audiobookshelf from your app store.</li>
          <li>Tap "Add a server", paste the URL above.</li>
          <li>Log in with your Silo username + password.</li>
          <li>Your audiobooks library will sync automatically.</li>
        </ol>
        <div className="flex flex-wrap gap-3 pt-2 text-sm">
          <a
            href="https://apps.apple.com/us/app/audiobookshelf/id1610126326"
            target="_blank"
            rel="noreferrer"
            className="text-primary inline-flex min-h-9 items-center gap-1 underline"
          >
            <Apple className="size-4" /> iOS App Store
          </a>
          <a
            href="https://play.google.com/store/apps/details?id=com.audiobookshelf.app"
            target="_blank"
            rel="noreferrer"
            className="text-primary inline-flex min-h-9 items-center gap-1 underline"
          >
            Google Play
          </a>
        </div>
      </Card>
      <MobileLoginOptIn />
    </div>
  );
}

function MobileLoginOptIn() {
  const qc = useQueryClient();
  const state = useQuery({
    queryKey: ['me', 'abs-standalone'],
    queryFn: () => api.getABSStandaloneOptIn(),
  });

  const enable = useMutation({
    mutationFn: () => api.enableABSStandaloneOptIn(),
    onSuccess: () => {
      toast.success('Mobile-app login enabled.');
      qc.invalidateQueries({ queryKey: ['me', 'abs-standalone'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const disable = useMutation({
    mutationFn: () => api.disableABSStandaloneOptIn(),
    onSuccess: () => {
      toast.success('Mobile-app login disabled.');
      qc.invalidateQueries({ queryKey: ['me', 'abs-standalone'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (state.isLoading || !state.data) return null;

  // Mode is set by the admin. When it's "disabled" or "all_accounts" the
  // listener has no per-account choice to make, so the toggle is hidden;
  // we still surface a short status line so the listener understands why.
  if (state.data.mode === 'disabled') {
    return (
      <Card className="bg-surface space-y-2 p-6">
        <h3 className="text-sm font-medium">Mobile-app login</h3>
        <p className="text-muted-foreground text-sm">
          Mobile-app login is currently turned off for this server. Use the in-browser player above, or
          ask your administrator to enable Audiobookshelf-client login.
        </p>
      </Card>
    );
  }
  if (state.data.mode === 'all_accounts') {
    return (
      <Card className="bg-surface space-y-2 p-6">
        <h3 className="text-sm font-medium">Mobile-app login</h3>
        <p className="text-muted-foreground text-sm">
          Mobile-app login is available to every account on this server. Use your Silo username and
          password directly in the Audiobookshelf app.
        </p>
      </Card>
    );
  }

  // mode === "opt_in" — show the per-listener switch.
  const pending = enable.isPending || disable.isPending;
  return (
    <Card className="bg-surface space-y-3 p-6">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <Label htmlFor="abs-mobile-login-toggle" className="text-sm font-medium">
            Allow mobile-app login for my account
          </Label>
          <p className="text-muted-foreground text-sm">
            When this is on, the Audiobookshelf app can sign in to this server with your Silo
            username and password. Leave it off if you only listen in the browser.
          </p>
        </div>
        <Switch
          id="abs-mobile-login-toggle"
          checked={state.data.enabled}
          disabled={pending}
          onCheckedChange={(next) => (next ? enable.mutate() : disable.mutate())}
        />
      </div>
    </Card>
  );
}
