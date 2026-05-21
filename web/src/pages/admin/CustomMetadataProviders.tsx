import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Plug, Plus, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import type { CustomMetadataProvider } from '@/api/types';

// Admin CRUD for custom metadata providers — external HTTP search
// endpoints conforming to the upstream custom-metadata-provider
// spec. The plugin proxies search requests to enabled providers
// (auth_header stays server-side; never leaks to clients).

export default function CustomMetadataProvidersTab() {
  const qc = useQueryClient();
  const providers = useQuery({
    queryKey: ['admin-custom-metadata-providers'],
    queryFn: () => api.listCustomMetadataProviders(),
  });
  const [editing, setEditing] = useState<CustomMetadataProvider | null>(null);
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [authHeader, setAuthHeader] = useState('');
  const [enabled, setEnabled] = useState(true);

  const create = useMutation({
    mutationFn: () =>
      api.createCustomMetadataProvider({
        name: name.trim(),
        url: url.trim(),
        auth_header: authHeader.trim(),
        enabled,
      }),
    onSuccess: () => {
      setName('');
      setUrl('');
      setAuthHeader('');
      setEnabled(true);
      qc.invalidateQueries({ queryKey: ['admin-custom-metadata-providers'] });
      toast.success('Provider added');
    },
    onError: (err) => toast.error(`Add failed: ${err}`),
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteCustomMetadataProvider(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin-custom-metadata-providers'] });
    },
  });

  const toggle = useMutation({
    mutationFn: (p: CustomMetadataProvider) =>
      api.updateCustomMetadataProvider(p.id, { enabled: !p.enabled }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin-custom-metadata-providers'] });
    },
  });

  return (
    <Card className="bg-surface p-4">
      <div className="mb-3 flex items-center gap-2">
        <Plug className="size-5" />
        <h3 className="font-medium">Custom metadata providers</h3>
      </div>
      <p className="text-muted-foreground mb-4 text-xs">
        External HTTP search endpoints that conform to the
        custom-metadata-provider spec. The plugin proxies search
        requests; auth headers never leave the server.
      </p>

      {providers.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (providers.data?.items ?? []).length === 0 ? (
        <p className="text-muted-foreground mb-4 text-sm">No providers yet.</p>
      ) : (
        <ul className="mb-4 space-y-2">
          {providers.data!.items.map((p) =>
            editing?.id === p.id ? (
              <ProviderEditor
                key={p.id}
                provider={p}
                onCancel={() => setEditing(null)}
                onSaved={() => {
                  setEditing(null);
                  qc.invalidateQueries({
                    queryKey: ['admin-custom-metadata-providers'],
                  });
                }}
              />
            ) : (
              <li
                key={p.id}
                className="bg-background flex items-center justify-between rounded-md border border-dashed p-3 text-sm"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 font-medium">
                    <span
                      className={
                        p.enabled
                          ? 'bg-emerald-500 size-2 rounded-full'
                          : 'bg-muted-foreground/40 size-2 rounded-full'
                      }
                      aria-hidden
                    />
                    {p.name}
                  </div>
                  <div className="text-muted-foreground truncate text-xs">
                    {p.url}
                    {p.auth_header ? ' · auth header set' : ''}
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  <Switch
                    checked={p.enabled}
                    onCheckedChange={() => toggle.mutate(p)}
                  />
                  <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>
                    Edit
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    onClick={() => remove.mutate(p.id)}
                    title="Remove"
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </li>
            ),
          )}
        </ul>
      )}

      <div className="bg-border my-4 h-px" />

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <Label htmlFor="prov-name">Name</Label>
          <Input
            id="prov-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Audnexus"
          />
        </div>
        <div>
          <Label htmlFor="prov-url">Base URL</Label>
          <Input
            id="prov-url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://api.audnex.us"
          />
        </div>
        <div className="sm:col-span-2">
          <Label htmlFor="prov-auth">Auth header (optional)</Label>
          <Input
            id="prov-auth"
            type="password"
            value={authHeader}
            onChange={(e) => setAuthHeader(e.target.value)}
            placeholder="Bearer …"
          />
        </div>
      </div>
      <div className="mt-3 flex items-center justify-between">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} />
          Enabled
        </label>
        <Button
          onClick={() => create.mutate()}
          disabled={!name.trim() || !url.trim() || create.isPending}
        >
          <Plus className="size-4" />
          <span className="ml-1">Add</span>
        </Button>
      </div>
    </Card>
  );
}

function ProviderEditor({
  provider,
  onCancel,
  onSaved,
}: {
  provider: CustomMetadataProvider;
  onCancel: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(provider.name);
  const [url, setUrl] = useState(provider.url);
  const [authHeader, setAuthHeader] = useState(provider.auth_header ?? '');
  const [enabled, setEnabled] = useState(provider.enabled);

  const save = useMutation({
    mutationFn: () =>
      api.updateCustomMetadataProvider(provider.id, {
        name: name.trim(),
        url: url.trim(),
        auth_header: authHeader.trim(),
        enabled,
      }),
    onSuccess: () => {
      toast.success('Saved');
      onSaved();
    },
    onError: (err) => toast.error(`Save failed: ${err}`),
  });

  return (
    <li className="bg-background space-y-2 rounded-md border border-dashed p-3 text-sm">
      <div className="grid gap-2 sm:grid-cols-2">
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name" />
        <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="URL" />
        <Input
          type="password"
          value={authHeader}
          onChange={(e) => setAuthHeader(e.target.value)}
          placeholder="Auth header"
          className="sm:col-span-2"
        />
      </div>
      <div className="flex items-center justify-between">
        <label className="flex items-center gap-2">
          <Switch checked={enabled} onCheckedChange={setEnabled} />
          Enabled
        </label>
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending}>
            Save
          </Button>
        </div>
      </div>
    </li>
  );
}
