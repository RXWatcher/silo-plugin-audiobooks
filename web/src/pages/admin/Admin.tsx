import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Database, HardDrive, LibraryBig, RefreshCw, Shield } from 'lucide-react';
import { toast } from 'sonner';

import { api, fetchInstalledBackends, fetchRequestProviders } from '@/api/client';
import type { InstalledBackend, UserRequest } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import AdminSettings from './Settings';

const REQUEST_STATUSES = [
  'pending',
  'submitted',
  'acknowledged',
  'queued',
  'downloading',
  'imported',
  'failed',
  'denied',
  'cancelled',
] as const;

export default function Admin() {
  const qc = useQueryClient();
  const [tab, setTab] = useState('libraries');

  const libraries = useQuery({
    queryKey: ['admin', 'libraries'],
    queryFn: () => api.adminListLibraries(),
  });
  const backends = useQuery({
    queryKey: ['admin', 'audiobook-backends'],
    queryFn: fetchInstalledBackends,
  });
  const providers = useQuery({
    queryKey: ['admin', 'audiobook-request-providers'],
    queryFn: fetchRequestProviders,
  });
  const pendingRequests = useQuery({
    queryKey: ['admin-requests', 'pending'],
    queryFn: () => api.adminListRequests('pending'),
  });
  const sessions = useQuery({
    queryKey: ['admin-sessions'],
    queryFn: () => api.adminListSessions(),
  });
  const tokens = useQuery({
    queryKey: ['admin-tokens', ''],
    queryFn: () => api.adminListTokens(),
  });

  const refreshAll = () => {
    qc.invalidateQueries({ queryKey: ['admin'] });
    qc.invalidateQueries({ queryKey: ['backend-config'] });
    qc.invalidateQueries({ queryKey: ['libraries'] });
  };

  const activeLibraries = (libraries.data?.items ?? []).filter((library) => library.enabled !== false)
    .length;
  const pendingCount = (pendingRequests.data?.items ?? []).length;
  const activeSessions = (sessions.data?.items ?? []).length;
  const liveTokens = (tokens.data?.items ?? []).filter((token) => !token.revoked_at).length;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Audiobooks Administration</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Manage presentation libraries, request flow, ABS access, and backend routing from one
            place.
          </p>
        </div>
        <Button type="button" variant="outline" onClick={refreshAll}>
          <RefreshCw className="size-4" />
          Refresh
        </Button>
      </header>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <MetricCard
          icon={<LibraryBig className="size-4" />}
          label="Enabled libraries"
          value={String(activeLibraries)}
          detail={`${(libraries.data?.items ?? []).length} presentation routes configured`}
        />
        <MetricCard
          icon={<Database className="size-4" />}
          label="Library sources"
          value={String(backends.data?.length ?? 0)}
          detail={`${providers.data?.length ?? 0} request providers`}
        />
        <MetricCard
          icon={<HardDrive className="size-4" />}
          label="Pending requests"
          value={String(pendingCount)}
          detail="Awaiting provider action or review"
        />
        <MetricCard
          icon={<Shield className="size-4" />}
          label="Active ABS access"
          value={String(liveTokens)}
          detail={`${activeSessions} live playback sessions`}
        />
      </div>

      <Tabs value={tab} onValueChange={setTab} className="space-y-4">
        <TabsList className="flex h-auto w-full flex-wrap justify-start">
          <TabsTrigger value="libraries">Libraries</TabsTrigger>
          <TabsTrigger value="requests">Requests</TabsTrigger>
          <TabsTrigger value="providers">Providers</TabsTrigger>
          <TabsTrigger value="sessions">Sessions</TabsTrigger>
          <TabsTrigger value="tokens">Tokens</TabsTrigger>
        </TabsList>

        <TabsContent value="libraries">
          <div className="space-y-4">
            {backends.error instanceof Error && (
              <InlineError>
                Couldn&apos;t load installed library sources: {backends.error.message}. The source
                dropdown below will be empty until this is resolved.
              </InlineError>
            )}
            <BackendsPanel backends={backends.data ?? []} />
            <AdminSettings />
          </div>
        </TabsContent>
        <TabsContent value="requests">
          <RequestsTab />
        </TabsContent>
        <TabsContent value="providers">
          <ProvidersTab
            providers={providers.data ?? []}
            error={providers.error instanceof Error ? providers.error.message : null}
          />
        </TabsContent>
        <TabsContent value="sessions">
          <SessionsTab />
        </TabsContent>
        <TabsContent value="tokens">
          <TokensTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function InlineError({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
      {children}
    </div>
  );
}

function MetricCard({
  icon,
  label,
  value,
  detail,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  detail: string;
}) {
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="pb-3">
        <CardDescription className="flex items-center gap-2 text-xs uppercase tracking-[0.18em]">
          <span className="text-primary">{icon}</span>
          {label}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-1 pb-6">
        <div className="text-3xl font-semibold tracking-tight">{value}</div>
        <p className="text-sm text-muted-foreground">{detail}</p>
      </CardContent>
    </Card>
  );
}

function RequestsTab() {
  const qc = useQueryClient();
  const [status, setStatus] = useState<(typeof REQUEST_STATUSES)[number]>('pending');
  const [denyTarget, setDenyTarget] = useState<UserRequest | null>(null);
  const [denyReason, setDenyReason] = useState('');

  const requests = useQuery({
    queryKey: ['admin-requests', status],
    queryFn: () => api.adminListRequests(status),
  });

  const approve = useMutation({
    mutationFn: (id: string) => api.adminApproveRequest(id),
    onSuccess: () => {
      toast.success('Request approved');
      qc.invalidateQueries({ queryKey: ['admin-requests'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deny = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.adminDenyRequest(id, reason),
    onSuccess: () => {
      toast.success('Request denied');
      setDenyTarget(null);
      setDenyReason('');
      qc.invalidateQueries({ queryKey: ['admin-requests'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle>Request queue</CardTitle>
        <CardDescription>
          Review incoming audiobook requests and manually approve or deny them when needed.
        </CardDescription>
        <div className="flex flex-wrap gap-2 pt-2">
          {REQUEST_STATUSES.map((item) => (
            <Button
              key={item}
              type="button"
              size="sm"
              variant={item === status ? 'default' : 'outline'}
              onClick={() => setStatus(item)}
            >
              {item}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="pt-5">
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Title</TableHead>
                <TableHead>Author</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Requester</TableHead>
                <TableHead>Submitted</TableHead>
                <TableHead className="w-48 text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(requests.data?.items ?? []).map((request) => (
                <TableRow key={request.id}>
                  <TableCell className="font-medium">{request.title}</TableCell>
                  <TableCell className="text-muted-foreground">{request.author || '—'}</TableCell>
                  <TableCell>
                    <Badge variant={request.status === 'pending' ? 'secondary' : 'outline'}>
                      {request.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {request.user_id}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {new Date(request.created_at).toLocaleString()}
                  </TableCell>
                  <TableCell className="text-right">
                    {request.status === 'pending' ? (
                      <div className="flex justify-end gap-2">
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() => approve.mutate(request.id)}
                          disabled={approve.isPending}
                        >
                          Approve
                        </Button>
                        <Button
                          type="button"
                          size="sm"
                          variant="destructive"
                          onClick={() => setDenyTarget(request)}
                          disabled={deny.isPending}
                        >
                          Deny
                        </Button>
                      </div>
                    ) : (
                      <span className="text-xs text-muted-foreground">
                        {request.denied_reason || request.failure_reason || request.external_id || '—'}
                      </span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {requests.data && requests.data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-sm text-muted-foreground">
                    No {status} requests.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </CardContent>

      <Dialog
        open={!!denyTarget}
        onOpenChange={(open) => {
          if (!open) {
            setDenyTarget(null);
            setDenyReason('');
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deny request</DialogTitle>
            <DialogDescription>
              {denyTarget ? `Add an optional reason for "${denyTarget.title}".` : ''}
            </DialogDescription>
          </DialogHeader>
          <Input
            value={denyReason}
            onChange={(e) => setDenyReason(e.target.value)}
            placeholder="Reason shown to admins"
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setDenyTarget(null)}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={() => {
                if (!denyTarget) return;
                deny.mutate({ id: denyTarget.id, reason: denyReason });
              }}
              disabled={deny.isPending}
            >
              Deny request
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function BackendsPanel({ backends }: { backends: InstalledBackend[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Available library sources</CardTitle>
        <CardDescription>
          These plugins can return catalog pages, audiobook details, covers, and files for the
          player UI. Request-only download providers are listed on the Providers tab instead.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {backends.length === 0 ? (
          <div className="rounded-lg border border-dashed p-8 text-sm text-muted-foreground">
            No library sources found. Install or enable a catalog-capable audiobook source before
            routing presentation libraries.
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Install ID</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Plugin ID</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {backends.map((backend) => (
                  <TableRow key={backend.id}>
                    <TableCell>{backend.id}</TableCell>
                    <TableCell className="font-medium">{backend.display_name}</TableCell>
                    <TableCell>{backend.plugin_id}</TableCell>
                    <TableCell>
                      <Badge variant="secondary">Enabled</Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ProvidersTab({
  providers,
  error,
}: {
  providers: InstalledBackend[];
  error: string | null;
}) {
  return (
    <div className="space-y-4">
      {error && (
        <InlineError>Couldn&apos;t load installed request providers: {error}</InlineError>
      )}
      <Card>
        <CardHeader>
          <CardTitle>Available request providers</CardTitle>
          <CardDescription>
            These plugins accept audiobook request events and report status back into the request
            queue. They are not used as reader-facing catalog sources unless they also advertise a
            library-source role.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {providers.length === 0 ? (
            <div className="rounded-lg border border-dashed p-8 text-sm text-muted-foreground">
              No request providers found. Install or enable a provider like Audiobook Requests
              before routing incoming requests.
            </div>
          ) : (
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Install ID</TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead>Plugin ID</TableHead>
                    <TableHead>Roles</TableHead>
                    <TableHead>Catalog</TableHead>
                    <TableHead>Requests</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {providers.map((provider) => (
                    <TableRow key={provider.id}>
                      <TableCell>{provider.id}</TableCell>
                      <TableCell className="font-medium">{provider.display_name}</TableCell>
                      <TableCell>{provider.plugin_id}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {provider.audiobook_roles.join(', ') || 'request_provider'}
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant={
                            provider.audiobook_backend?.metadata?.supports_catalog === true
                              ? 'secondary'
                              : 'outline'
                          }
                        >
                          {provider.audiobook_backend?.metadata?.supports_catalog === true
                            ? 'Yes'
                            : 'No'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant={
                            provider.audiobook_backend?.metadata?.supports_requests === false
                              ? 'outline'
                              : 'secondary'
                          }
                        >
                          {provider.audiobook_backend?.metadata?.supports_requests === false
                            ? 'No'
                            : 'Yes'}
                        </Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function SessionsTab() {
  const qc = useQueryClient();
  const sessions = useQuery({
    queryKey: ['admin-sessions'],
    queryFn: () => api.adminListSessions(),
  });

  const close = useMutation({
    mutationFn: (id: string) => api.adminCloseSession(id),
    onSuccess: () => {
      toast.success('Session closed');
      qc.invalidateQueries({ queryKey: ['admin-sessions'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle>ABS sessions</CardTitle>
        <CardDescription>
          Close stale or broken mobile-client playback sessions without touching the portal config.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-5">
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Book</TableHead>
                <TableHead>Device</TableHead>
                <TableHead>Player</TableHead>
                <TableHead>Last update</TableHead>
                <TableHead className="w-24 text-right">Action</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(sessions.data?.items ?? []).map((session) => (
                <TableRow key={session.id}>
                  <TableCell className="font-mono text-xs">{session.user_id}</TableCell>
                  <TableCell className="font-mono text-xs">{session.book_id}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{session.device_id}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {session.media_player || '—'}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {new Date(session.last_update).toLocaleString()}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      onClick={() => close.mutate(session.id)}
                      disabled={close.isPending}
                    >
                      Close
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {sessions.data && sessions.data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-sm text-muted-foreground">
                    No active sessions.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

function TokensTab() {
  const qc = useQueryClient();
  const [userId, setUserId] = useState('');

  const tokens = useQuery({
    queryKey: ['admin-tokens', userId],
    queryFn: () => api.adminListTokens(userId || undefined),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => api.adminRevokeToken(id),
    onSuccess: () => {
      toast.success('Token revoked');
      qc.invalidateQueries({ queryKey: ['admin-tokens'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const activeCount = useMemo(
    () => (tokens.data?.items ?? []).filter((token) => !token.revoked_at).length,
    [tokens.data?.items],
  );

  return (
    <Card>
      <CardHeader className="border-b">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle>ABS tokens</CardTitle>
            <CardDescription>
              Review mobile auth tokens, filter by user, and revoke any lingering device access.
            </CardDescription>
          </div>
          <Badge variant="secondary">{activeCount} active</Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4 pt-5">
        <Input
          value={userId}
          onChange={(e) => setUserId(e.target.value)}
          placeholder="Filter by user id"
          className="max-w-xs"
        />
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>JTI</TableHead>
                <TableHead>Device</TableHead>
                <TableHead>Expires</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="w-24 text-right">Action</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(tokens.data?.items ?? []).map((token) => (
                <TableRow key={token.id}>
                  <TableCell className="font-mono text-xs">{token.user_id}</TableCell>
                  <TableCell className="font-mono text-xs">{token.jti.slice(0, 12)}…</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {token.device_name || token.device_id || '—'}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {new Date(token.expires_at).toLocaleDateString()}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {token.last_used_at ? new Date(token.last_used_at).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell className="text-right">
                    {!token.revoked_at ? (
                      <Button
                        type="button"
                        size="sm"
                        variant="ghost"
                        onClick={() => revoke.mutate(token.id)}
                        disabled={revoke.isPending}
                      >
                        Revoke
                      </Button>
                    ) : (
                      <span className="text-xs text-muted-foreground">Revoked</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {tokens.data && tokens.data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-sm text-muted-foreground">
                    No tokens found.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}
