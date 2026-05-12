import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';

export default function AdminTokens() {
  const qc = useQueryClient();
  const [userId, setUserId] = useState('');
  const q = useQuery({
    queryKey: ['admin-tokens', userId],
    queryFn: () => api.adminListTokens(userId || undefined),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.adminRevokeToken(id),
    onSuccess: () => {
      toast.success('Revoked');
      qc.invalidateQueries({ queryKey: ['admin-tokens'] });
    },
    onError: (e) => toast.error(`${e}`),
  });

  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Admin: ABS tokens</h2>
      <div className="flex gap-2">
        <Input
          value={userId}
          onChange={(e) => setUserId(e.target.value)}
          placeholder="Filter by user id"
          className="max-w-xs"
        />
      </div>
      <div className="bg-surface overflow-hidden rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>User</TableHead>
              <TableHead>JTI</TableHead>
              <TableHead>Device</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {(q.data?.items ?? []).map((t) => (
              <TableRow key={t.id}>
                <TableCell className="font-mono text-xs">{t.user_id}</TableCell>
                <TableCell className="font-mono text-xs">{t.jti.slice(0, 12)}…</TableCell>
                <TableCell className="text-muted-foreground text-xs">{t.device_name || t.device_id || '—'}</TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {new Date(t.expires_at).toLocaleDateString()}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {t.last_used_at ? new Date(t.last_used_at).toLocaleString() : '—'}
                </TableCell>
                <TableCell>
                  {!t.revoked_at && (
                    <Button size="sm" variant="ghost" onClick={() => revoke.mutate(t.id)}>
                      Revoke
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {q.data && q.data.items.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
                  No tokens.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
