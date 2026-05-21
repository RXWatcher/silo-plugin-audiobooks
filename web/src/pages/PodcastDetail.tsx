import { useRef, useState } from 'react';
import { useParams, Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Mic, Play, Pause } from 'lucide-react';
import { api } from '@/api/client';
import { Card } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import type { PodcastEpisode } from '@/api/types';

// PodcastDetail renders one podcast's metadata header and its full
// episode list. Each episode row has an inline play control that
// streams the original audio URL through a native <audio> element —
// podcast audio lives on external CDNs (the feed refresher copies the
// enclosure URL straight into store.PodcastEpisode.AudioURL), so the
// SPA doesn't need to round-trip through the plugin's stream proxy
// the way audiobook playback does.
export default function PodcastDetail() {
  const { id = '' } = useParams();
  const podcast = useQuery({
    queryKey: ['podcast', id],
    queryFn: () => api.getPodcast(id),
    enabled: !!id,
  });
  const episodes = useQuery({
    queryKey: ['podcast-episodes', id],
    queryFn: () => api.listPodcastEpisodes(id),
    enabled: !!id,
  });

  if (podcast.isLoading) return <DetailSkeleton />;
  if (podcast.isError || !podcast.data)
    return (
      <Card className="bg-surface p-6 text-sm">
        Podcast not found. <Link to="/podcasts" className="underline">Back to podcasts</Link>.
      </Card>
    );

  const p = podcast.data;
  const items = episodes.data?.items ?? [];
  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:gap-6">
        <div className="bg-muted relative aspect-square w-32 shrink-0 overflow-hidden rounded-lg sm:w-44">
          {p.cover_url ? (
            <img
              src={p.cover_url}
              alt={`${p.title} cover`}
              loading="lazy"
              className="size-full object-cover"
            />
          ) : (
            <div className="text-muted-foreground flex size-full items-center justify-center">
              <Mic className="size-12" />
            </div>
          )}
        </div>
        <div className="space-y-1">
          <h2 className="text-2xl font-semibold tracking-tight">{p.title}</h2>
          {p.author && <div className="text-muted-foreground">{p.author}</div>}
          <div className="text-muted-foreground text-xs">
            {items.length} {items.length === 1 ? 'episode' : 'episodes'}
            {p.last_refreshed_at &&
              ` · last refreshed ${new Date(p.last_refreshed_at).toLocaleString()}`}
          </div>
        </div>
      </div>

      {p.description && (
        <Card className="bg-surface space-y-2 p-4">
          <h3 className="text-muted-foreground text-xs uppercase tracking-wide">About</h3>
          <p className="text-sm whitespace-pre-wrap">{p.description}</p>
        </Card>
      )}

      <div className="space-y-3">
        <h3 className="text-lg font-semibold">Episodes</h3>
        {items.length === 0 ? (
          <Card className="bg-surface p-4 text-sm">
            No episodes yet. The feed refresher runs every 10 minutes; new
            episodes appear automatically once they're published.
          </Card>
        ) : (
          <div className="space-y-2">
            {items.map((e) => (
              <EpisodeRow key={e.id} episode={e} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function EpisodeRow({ episode }: { episode: PodcastEpisode }) {
  // Each row carries its own <audio> ref so multiple episodes can be
  // queued without bleeding state between rows. The audio_url comes
  // straight from the feed enclosure — the SPA plays it directly, no
  // session-mint or plugin-proxy hop.
  const [playing, setPlaying] = useState(false);
  const audioRef = useRef<HTMLAudioElement | null>(null);

  const toggle = () => {
    const el = audioRef.current;
    if (!el) return;
    if (el.paused) {
      void el.play();
      setPlaying(true);
    } else {
      el.pause();
      setPlaying(false);
    }
  };

  return (
    <Card className="bg-surface flex gap-3 p-3">
      <Button
        size="icon"
        onClick={toggle}
        variant={playing ? 'secondary' : 'default'}
        className="size-10 shrink-0 rounded-full"
        aria-label={playing ? 'Pause episode' : 'Play episode'}
      >
        {playing ? <Pause className="size-5" /> : <Play className="size-5" />}
      </Button>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex items-baseline gap-2">
          <div className="line-clamp-2 text-sm font-medium">{episode.title}</div>
          {episode.episode_index && (
            <span className="text-muted-foreground shrink-0 text-xs">
              #{episode.episode_index}
            </span>
          )}
        </div>
        {episode.published_at && (
          <div className="text-muted-foreground text-xs">
            {new Date(episode.published_at).toLocaleDateString()}
            {episode.duration_seconds > 0 && ` · ${formatDuration(episode.duration_seconds)}`}
          </div>
        )}
        {episode.description && (
          <p className="text-muted-foreground line-clamp-3 text-xs">{episode.description}</p>
        )}
        <audio
          ref={audioRef}
          src={episode.audio_url}
          preload="none"
          onEnded={() => setPlaying(false)}
          onPause={() => setPlaying(false)}
          onPlay={() => setPlaying(true)}
          controls
          className="mt-2 w-full"
        />
      </div>
    </Card>
  );
}

function formatDuration(sec: number): string {
  if (sec <= 0) return '';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}h ${m.toString().padStart(2, '0')}m`;
  return `${m}m ${s.toString().padStart(2, '0')}s`;
}

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <div className="flex gap-4">
        <Skeleton className="aspect-square w-44" />
        <div className="space-y-2">
          <Skeleton className="h-6 w-64" />
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-3 w-24" />
        </div>
      </div>
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-16 w-full" />
      <Skeleton className="h-16 w-full" />
    </div>
  );
}
