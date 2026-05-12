import { useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import AudiobookGrid from '@/components/AudiobookGrid';

export default function SeriesDetail() {
  const { id = '' } = useParams();
  const q = useQuery({
    queryKey: ['series', id],
    queryFn: () => api.listAudiobooks({ q: id, limit: 100 }),
    enabled: !!id,
  });
  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-semibold">Series</h2>
      <AudiobookGrid items={q.data?.items ?? []} loading={q.isLoading} empty="No books in this series." />
    </div>
  );
}
