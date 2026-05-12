import { useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import AudiobookGrid from '@/components/AudiobookGrid';

export default function NarratorDetail() {
  const { id = '' } = useParams();
  const q = useQuery({
    queryKey: ['narrator', id],
    queryFn: () => api.listAudiobooks({ q: id, limit: 100 }),
    enabled: !!id,
  });
  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-semibold">Narrator</h2>
      <AudiobookGrid items={q.data?.items ?? []} loading={q.isLoading} empty="No books for this narrator." />
    </div>
  );
}
