import { useState } from 'react';
import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Button } from '@/components/ui/button';

const STATUSES = ['pending', 'submitted', 'acknowledged', 'imported', 'failed', 'denied', 'cancelled'];

export default function AdminRequests() {
  const [status, setStatus] = useState('pending');
  const q = useQuery({
    queryKey: ['admin-requests', status],
    queryFn: () => api.adminListRequests(status),
  });

  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Admin: Requests</h2>
      <Tabs value={status} onValueChange={setStatus}>
        <TabsList>
          {STATUSES.map((s) => (
            <TabsTrigger key={s} value={s}>
              {s}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      <div className="bg-surface overflow-hidden rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Title</TableHead>
              <TableHead>Author</TableHead>
              <TableHead>Requester</TableHead>
              <TableHead>Submitted</TableHead>
              <TableHead className="w-32" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {(q.data?.items ?? []).map((r) => (
              <TableRow key={r.id}>
                <TableCell>{r.title}</TableCell>
                <TableCell className="text-muted-foreground">{r.author || ''}</TableCell>
                <TableCell className="text-muted-foreground font-mono text-xs">{r.user_id}</TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {new Date(r.created_at).toLocaleString()}
                </TableCell>
                <TableCell>
                  <Button asChild size="sm" variant="outline">
                    <Link to={`/admin/requests/${encodeURIComponent(r.id)}`}>Review</Link>
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {q.data && q.data.items.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-muted-foreground py-8 text-center">
                  No {status} requests.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
