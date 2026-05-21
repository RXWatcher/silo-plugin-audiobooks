import { useMemo } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Bell, Copy, Link2, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import type { ShareLink } from '@/api/types';

// User-facing settings page. Today it surfaces notification
// preferences; future sections (share links, content restriction,
// integrations) bolt onto the same shell as separate Cards.

const CATEGORY_LABELS: Record<string, string> = {
  new_book: 'A new audiobook lands in a library you can see',
  new_episode: 'A podcast you subscribe to has new episodes',
  request_fulfilled: 'An audiobook you requested arrives',
  backup_complete: 'Admin: backup job completes',
  share_used: 'Someone opens a share link you created',
};

const DELIVERY_LABELS: Record<string, string> = {
  inapp: 'In-app',
  email: 'Email',
  push: 'Push',
};

export default function Settings() {
  return (
    <div className="space-y-6">
      <header>
        <h2 className="text-2xl font-semibold tracking-tight">Settings</h2>
        <p className="text-muted-foreground text-sm">
          Manage notification preferences and other personal settings.
        </p>
      </header>
      <ShareLinksCard />
      <NotificationPrefsCard />
    </div>
  );
}

function ShareLinksCard() {
  const qc = useQueryClient();
  const links = useQuery({
    queryKey: ['share-links'],
    queryFn: () => api.listShareLinks(),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteShareLink(id),
    onSuccess: () => {
      toast.success('Share link revoked');
      qc.invalidateQueries({ queryKey: ['share-links'] });
    },
    onError: (err) => toast.error(`Revoke failed: ${err}`),
  });

  return (
    <Card className="bg-surface p-4">
      <div className="mb-4 flex items-center gap-2">
        <Link2 className="size-5" />
        <h3 className="font-medium">Share links</h3>
      </div>
      <p className="text-muted-foreground mb-3 text-xs">
        Anyone with one of these links can play the linked audiobook until it
        expires or hits the use cap. Revoking instantly disables a link.
      </p>

      {links.isLoading ? (
        <Skeleton className="h-20 w-full" />
      ) : (links.data?.items ?? []).length === 0 ? (
        <p className="text-muted-foreground text-sm">No share links yet.</p>
      ) : (
        <ul className="space-y-2">
          {links.data!.items.map((l) => (
            <ShareLinkRow key={l.id} link={l} onDelete={() => remove.mutate(l.id)} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function ShareLinkRow({
  link,
  onDelete,
}: {
  link: ShareLink;
  onDelete: () => void;
}) {
  const url = `${window.location.origin}/share/${link.slug}`;
  const expiresInDays = link.expires_at
    ? Math.max(
        0,
        Math.ceil(
          (new Date(link.expires_at).getTime() - Date.now()) / (1000 * 60 * 60 * 24),
        ),
      )
    : null;
  const usesRemaining =
    link.max_uses > 0 ? Math.max(0, link.max_uses - link.use_count) : null;
  return (
    <li className="bg-background flex items-center justify-between gap-2 rounded-md border border-dashed p-3 text-sm">
      <div className="min-w-0 flex-1">
        <div className="truncate font-medium">{link.item_id}</div>
        <div className="text-muted-foreground text-xs">
          {expiresInDays !== null
            ? `Expires in ${expiresInDays} day${expiresInDays === 1 ? '' : 's'}`
            : 'No expiry'}
          {' · '}
          {usesRemaining !== null
            ? `${usesRemaining} use${usesRemaining === 1 ? '' : 's'} left`
            : `${link.use_count} opens`}
        </div>
      </div>
      <Button
        size="icon"
        variant="ghost"
        title="Copy link"
        onClick={() => {
          navigator.clipboard.writeText(url).then(
            () => toast.success('Link copied'),
            () => toast.error('Copy failed'),
          );
        }}
      >
        <Copy className="size-4" />
      </Button>
      <Button size="icon" variant="ghost" title="Revoke" onClick={onDelete}>
        <Trash2 className="size-4" />
      </Button>
    </li>
  );
}

function NotificationPrefsCard() {
  const qc = useQueryClient();
  const catalog = useQuery({
    queryKey: ['notification-catalog'],
    queryFn: () => api.getNotificationCatalog(),
  });
  const prefs = useQuery({
    queryKey: ['notification-prefs'],
    queryFn: () => api.listNotificationPrefs(),
  });

  const setPref = useMutation({
    mutationFn: (vars: { category: string; delivery: string; enabled: boolean }) =>
      api.putNotificationPref(vars.category, vars.delivery, vars.enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notification-prefs'] }),
    onError: (err) => toast.error(`Update failed: ${err}`),
  });

  // Build an enabled-map from current rows so the toggle reflects
  // server state. Missing rows are enabled by default (opt-out).
  const enabledMap = useMemo(() => {
    const m = new Map<string, boolean>();
    for (const p of prefs.data?.items ?? []) {
      m.set(`${p.category}/${p.delivery}`, p.enabled);
    }
    return m;
  }, [prefs.data]);

  const loading = catalog.isLoading || prefs.isLoading;

  return (
    <Card className="bg-surface p-4">
      <div className="mb-4 flex items-center gap-2">
        <Bell className="size-5" />
        <h3 className="font-medium">Notifications</h3>
      </div>

      {loading ? (
        <Skeleton className="h-32 w-full" />
      ) : (
        <div className="space-y-3">
          {(catalog.data?.categories ?? []).map((category) => (
            <CategoryRow
              key={category}
              category={category}
              deliveries={catalog.data?.deliveries ?? []}
              isEnabled={(delivery) =>
                enabledMap.get(`${category}/${delivery}`) ?? true
              }
              onToggle={(delivery, enabled) =>
                setPref.mutate({ category, delivery, enabled })
              }
            />
          ))}
          {(catalog.data?.categories ?? []).length === 0 && (
            <p className="text-muted-foreground text-sm">
              No notification categories configured.
            </p>
          )}
        </div>
      )}
    </Card>
  );
}

function CategoryRow({
  category,
  deliveries,
  isEnabled,
  onToggle,
}: {
  category: string;
  deliveries: string[];
  isEnabled: (delivery: string) => boolean;
  onToggle: (delivery: string, enabled: boolean) => void;
}) {
  return (
    <div className="bg-background flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed p-3">
      <div className="min-w-0 flex-1">
        <div className="font-medium text-sm">
          {CATEGORY_LABELS[category] ?? category}
        </div>
        <div className="text-muted-foreground text-xs">{category}</div>
      </div>
      <div className="flex gap-4">
        {deliveries.map((delivery) => (
          <label key={delivery} className="flex items-center gap-2">
            <Switch
              checked={isEnabled(delivery)}
              onCheckedChange={(v) => onToggle(delivery, v)}
            />
            <span className="text-xs">{DELIVERY_LABELS[delivery] ?? delivery}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
