import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { api, fetchInstalledBackends } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type {
  BackendConfig,
  InstalledBackend,
  LibraryInfo,
  StandaloneLoginMode,
} from '@/api/types';
import { STANDALONE_LOGIN_MODES } from '@/api/types';

const STANDALONE_LOGIN_MODE_LABELS: Record<StandaloneLoginMode, string> = {
  disabled: 'Disabled (host-proxied login only)',
  opt_in: 'Opt-in (each listener enables it from their account)',
  all_accounts: 'All accounts with a local password',
};

const STANDALONE_LOGIN_MODE_DESCRIPTION =
  'Controls whether the standalone listener accepts username + password from Audiobookshelf clients. ' +
  'Listeners without a local password on the Continuum host always fail closed regardless of mode.';

const MODES = ['proxy', 'cache', 'direct'] as const;
const MEDIA_TYPES = ['audiobook', 'podcast', 'audio drama', 'lecture'] as const;

export default function AdminSettings() {
  const qc = useQueryClient();
  const backend = useQuery({ queryKey: ['backend-config'], queryFn: () => api.getBackendConfig() });
  const providers = useQuery({ queryKey: ['admin', 'audiobook-backends'], queryFn: fetchInstalledBackends });
  const libraries = useQuery({ queryKey: ['admin', 'libraries'], queryFn: () => api.adminListLibraries() });
  const [form, setForm] = useState<BackendConfig | null>(null);
  const [draftLibraries, setDraftLibraries] = useState<LibraryInfo[]>([]);

  useEffect(() => {
    if (backend.data && !form) setForm(backend.data);
  }, [backend.data, form]);

  useEffect(() => {
    if (libraries.data) {
      setDraftLibraries(
        libraries.data.items.map((lib, index) => ({
          ...lib,
          media_type: lib.media_type || 'audiobook',
          enabled: lib.enabled ?? true,
          sort_order: lib.sort_order ?? index,
        })),
      );
    }
  }, [libraries.data]);

  const saveBackend = useMutation({
    mutationFn: (body: Partial<BackendConfig> & { rotate_abs_secret?: boolean }) =>
      api.updateBackendConfig(body),
    onSuccess: () => {
      toast.success('Settings saved');
      qc.invalidateQueries({ queryKey: ['backend-config'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const saveLibraries = useMutation({
    mutationFn: (items: LibraryInfo[]) => api.adminReplaceLibraries(items),
    onSuccess: () => {
      toast.success('Libraries saved');
      qc.invalidateQueries({ queryKey: ['admin', 'libraries'] });
      qc.invalidateQueries({ queryKey: ['libraries'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (!form) return <div className="text-muted-foreground">Loading...</div>;

  const update = <K extends keyof BackendConfig>(k: K, v: BackendConfig[K]) =>
    setForm({ ...form, [k]: v });
  const requestProviders = (providers.data ?? []).filter(isRequestProvider);

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold">Audiobooks Admin</h2>
        <p className="text-muted-foreground mt-1 text-sm">
          Configure presentation libraries, backend routing, streaming behavior, and client access.
        </p>
      </div>

      <LibrarySection
        libraries={draftLibraries}
        providers={providers.data ?? []}
        loading={libraries.isLoading || providers.isLoading}
        defaultBackendID={form.target_backend_plugin_id}
        onChange={setDraftLibraries}
        onSave={() => saveLibraries.mutate(normalizeLibraries(draftLibraries))}
        saving={saveLibraries.isPending}
      />

      <form
        onSubmit={(e) => {
          e.preventDefault();
          saveBackend.mutate(form);
        }}
        className="space-y-6"
      >
        <section className="bg-surface space-y-4 rounded-lg border p-6">
          <h3 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
            Request routing
          </h3>
          <Field label="Default provider">
            <ProviderSelect
              value={form.target_request_provider_plugin_id || form.target_backend_plugin_id}
              providers={requestProviders.length ? requestProviders : providers.data ?? []}
              onChange={(value) => update('target_request_provider_plugin_id', value)}
              placeholder="continuum.audiobook-requests"
            />
          </Field>
          <Field label="Auto-approve requests">
            <Switch
              checked={form.auto_approve_requests}
              onCheckedChange={(v) => update('auto_approve_requests', v)}
            />
          </Field>
        </section>

        <section className="bg-surface space-y-4 rounded-lg border p-6">
          <h3 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">Streaming</h3>
          <Field label="Mode">
            <Select
              value={form.streaming_mode}
              onValueChange={(v) => update('streaming_mode', v as BackendConfig['streaming_mode'])}
            >
              <SelectTrigger className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    {m}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {form.streaming_mode === 'cache' && (
            <>
              <Field label="Cache directory">
                <Input
                  value={form.cache_dir}
                  onChange={(e) => update('cache_dir', e.target.value)}
                  placeholder="/var/lib/continuum/audiobooks-cache"
                />
              </Field>
              <Field label="Max size (GB)">
                <Input
                  type="number"
                  value={form.cache_max_size_gb}
                  onChange={(e) => update('cache_max_size_gb', Number(e.target.value))}
                />
              </Field>
              <Field label="Download concurrency">
                <Input
                  type="number"
                  value={form.cache_download_concurrency}
                  onChange={(e) => update('cache_download_concurrency', Number(e.target.value))}
                />
              </Field>
            </>
          )}
        </section>

        <section className="bg-surface space-y-4 rounded-lg border p-6">
          <h3 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">ABS API</h3>
          <Field label="Access token TTL (hours)">
            <Input
              type="number"
              value={form.abs_access_token_ttl_hours}
              onChange={(e) => update('abs_access_token_ttl_hours', Number(e.target.value))}
            />
          </Field>
          <Field label="Refresh token TTL (days)">
            <Input
              type="number"
              value={form.abs_refresh_token_ttl_days}
              onChange={(e) => update('abs_refresh_token_ttl_days', Number(e.target.value))}
            />
          </Field>
          <Field label="Standalone listen address">
            <Input
              value={form.standalone_http_listen || ''}
              onChange={(e) => update('standalone_http_listen', e.target.value)}
              placeholder="127.0.0.1:9999"
            />
          </Field>
          <Field label="Mobile-app login" description={STANDALONE_LOGIN_MODE_DESCRIPTION}>
            <Select
              value={form.standalone_login_mode ?? 'disabled'}
              onValueChange={(v) => update('standalone_login_mode', v as StandaloneLoginMode)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STANDALONE_LOGIN_MODES.map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {STANDALONE_LOGIN_MODE_LABELS[mode]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Button
            type="button"
            variant="outline"
            onClick={() => saveBackend.mutate({ rotate_abs_secret: true })}
            disabled={saveBackend.isPending}
          >
            Rotate ABS signing secret
          </Button>
        </section>

        <Button type="submit" disabled={saveBackend.isPending}>
          {saveBackend.isPending ? 'Saving...' : 'Save settings'}
        </Button>
      </form>
    </div>
  );
}

function LibrarySection({
  libraries,
  providers,
  loading,
  defaultBackendID,
  onChange,
  onSave,
  saving,
}: {
  libraries: LibraryInfo[];
  providers: InstalledBackend[];
  loading: boolean;
  defaultBackendID: string;
  onChange: (items: LibraryInfo[]) => void;
  onSave: () => void;
  saving: boolean;
}) {
  const providerIDs = useMemo(() => providers.map((p) => p.plugin_id), [providers]);

  const updateLibrary = (index: number, patch: Partial<LibraryInfo>) => {
    onChange(libraries.map((lib, i) => (i === index ? { ...lib, ...patch } : lib)));
  };

  const addLibrary = () => {
    onChange([
      ...libraries,
      {
        id: 0,
        name: 'New audiobook library',
        media_type: 'audiobook',
        backend_plugin_id: defaultBackendID || providerIDs[0] || '',
        enabled: true,
        sort_order: libraries.length,
      },
    ]);
  };

  return (
    <section className="bg-surface space-y-4 rounded-lg border p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
            Presentation libraries
          </h3>
          <p className="text-muted-foreground mt-1 text-sm">
            Define the user-facing audiobook shelves and choose which backend powers each one.
          </p>
        </div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={addLibrary}>
            <Plus className="mr-2 size-4" />
            Add library
          </Button>
          <Button type="button" onClick={onSave} disabled={saving || loading}>
            {saving ? 'Saving...' : 'Save libraries'}
          </Button>
        </div>
      </div>

      {libraries.length === 0 ? (
        <div className="text-muted-foreground rounded-lg border border-dashed p-8 text-sm">
          No libraries configured. Add at least one library before exposing the portal.
        </div>
      ) : (
        <div className="space-y-3">
          {libraries.map((library, index) => (
            <div key={`${library.id || 'new'}-${index}`} className="rounded-lg border bg-background p-4">
              <div className="grid gap-3 lg:grid-cols-[1.2fr_1fr_1fr_auto]">
                <FieldStack label="Name">
                  <Input
                    value={library.name}
                    onChange={(e) => updateLibrary(index, { name: e.target.value })}
                  />
                </FieldStack>
                <FieldStack label="Media type">
                  <Select
                    value={library.media_type || 'audiobook'}
                    onValueChange={(v) => updateLibrary(index, { media_type: v })}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {MEDIA_TYPES.map((type) => (
                        <SelectItem key={type} value={type}>
                          {type}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </FieldStack>
                <FieldStack label="Source backend">
                  <ProviderSelect
                    value={library.backend_plugin_id || ''}
                    providers={providers}
                    onChange={(value) => updateLibrary(index, { backend_plugin_id: value })}
                    placeholder="continuum.local-audiobooks"
                  />
                </FieldStack>
                <div className="flex items-end gap-3">
                  <label className="flex items-center gap-2 pb-2 text-sm">
                    <Switch
                      checked={library.enabled ?? true}
                      onCheckedChange={(checked) => updateLibrary(index, { enabled: checked })}
                    />
                    Enabled
                  </label>
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => onChange(libraries.filter((_, i) => i !== index))}
                    aria-label="Remove library"
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function ProviderSelect({
  value,
  providers,
  onChange,
  placeholder,
}: {
  value: string;
  providers: InstalledBackend[];
  onChange: (value: string) => void;
  placeholder: string;
}) {
  if (providers.length === 0) {
    return <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />;
  }
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {providers.map((provider) => (
          <SelectItem key={provider.plugin_id} value={provider.plugin_id}>
            {provider.display_name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function isRequestProvider(provider: InstalledBackend): boolean {
  const roles = provider.audiobook_backend?.metadata?.audiobook_roles;
  return Array.isArray(roles) && roles.includes('request_provider');
}

function normalizeLibraries(items: LibraryInfo[]): LibraryInfo[] {
  return items.map((item, index) => ({
    ...item,
    media_type: item.media_type || 'audiobook',
    enabled: item.enabled ?? true,
    sort_order: index,
  }));
}

function Field({
  label,
  children,
  description,
}: {
  label: string;
  children: React.ReactNode;
  description?: string;
}) {
  return (
    <div className="grid gap-2 sm:grid-cols-3 sm:items-start">
      <div className="sm:col-span-1">
        <Label>{label}</Label>
        {description ? (
          <p className="text-muted-foreground mt-1 text-xs">{description}</p>
        ) : null}
      </div>
      <div className="sm:col-span-2">{children}</div>
    </div>
  );
}

function FieldStack({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  );
}
