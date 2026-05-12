import { useState } from 'react';
import { useParams, useNavigate } from 'react-router';
import { useMutation } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

export default function AdminRequestDetail() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [reason, setReason] = useState('');

  const approve = useMutation({
    mutationFn: () => api.adminApproveRequest(id),
    onSuccess: () => {
      toast.success('Approved');
      navigate('/admin');
    },
    onError: (e) => toast.error(`${e}`),
  });
  const deny = useMutation({
    mutationFn: () => api.adminDenyRequest(id, reason),
    onSuccess: () => {
      toast.success('Denied');
      navigate('/admin');
    },
    onError: (e) => toast.error(`${e}`),
  });

  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-semibold">Request #{id}</h2>
      <div className="bg-surface space-y-4 rounded-lg border p-6">
        <Button onClick={() => approve.mutate()} disabled={approve.isPending}>
          Approve
        </Button>
        <div className="space-y-2">
          <Input
            placeholder="Deny reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <Button variant="destructive" onClick={() => deny.mutate()} disabled={deny.isPending}>
            Deny
          </Button>
        </div>
      </div>
    </div>
  );
}
