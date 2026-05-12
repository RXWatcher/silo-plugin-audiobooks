import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';

export default function AdminSessions() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ['admin-sessions'], queryFn: () => api.adminListSessions() });
  const close = useMutation({
    mutationFn: (id: string) => api.adminCloseSession(id),
    onSuccess: () => {
      toast.success('Closed');
      qc.invalidateQueries({ queryKey: ['admin-sessions'] });
    },
    onError: (e) => toast.error(`${e}`),
  });

  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Admin: ABS sessions</h2>
      <div className="bg-surface overflow-hidden rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>User</TableHead>
              <TableHead>Book</TableHead>
              <TableHead>Device</TableHead>
              <TableHead>Player</TableHead>
              <TableHead>Last update</TableHead>
              <TableHead className="w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {(q.data?.items ?? []).map((s) => (
              <TableRow key={s.id}>
                <TableCell className="font-mono text-xs">{s.user_id}</TableCell>
                <TableCell className="font-mono text-xs">{s.book_id}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{s.device_id}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{s.media_player || '—'}</TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {new Date(s.last_update).toLocaleString()}
                </TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => close.mutate(s.id)}>
                    Close
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {q.data && q.data.items.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
                  No active sessions.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
