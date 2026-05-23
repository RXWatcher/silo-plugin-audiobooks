import { Routes, Route, Navigate } from 'react-router';
import Layout from '@/components/Layout';
import Home from '@/pages/Home';
import Library from '@/pages/Library';
import Detail from '@/pages/Detail';
import Series from '@/pages/Series';
import SeriesDetail from '@/pages/SeriesDetail';
import Authors from '@/pages/Authors';
import AuthorDetail from '@/pages/AuthorDetail';
import Narrators from '@/pages/Narrators';
import NarratorDetail from '@/pages/NarratorDetail';
import Collections from '@/pages/Collections';
import CollectionDetail from '@/pages/CollectionDetail';
import Podcasts from '@/pages/Podcasts';
import PodcastDetail from '@/pages/PodcastDetail';
import SmartCollections from '@/pages/SmartCollections';
import SmartCollectionDetail from '@/pages/SmartCollectionDetail';
import Stats from '@/pages/Stats';
import Settings from '@/pages/Settings';
import Apps from '@/pages/Apps';
import MyRequests from '@/pages/MyRequests';
import Admin from '@/pages/admin/Admin';
import { Toaster } from '@/components/ui/sonner';
import { PlaybackProvider } from '@/player/PlaybackProvider';

export default function App() {
  return (
    <PlaybackProvider>
      <Routes>
        <Route
          path="/admin"
          element={
            <main className="min-h-[100dvh] bg-background text-foreground">
              <div className="mx-auto max-w-[1600px] space-y-6 px-4 py-6 md:px-6 lg:px-8">
                <a
                  href="/admin/plugins"
                  className="text-muted-foreground hover:bg-surface-hover hover:text-foreground inline-flex min-h-9 items-center justify-center gap-1.5 rounded-lg px-2 py-1.5 text-xs font-medium transition-colors"
                  title="Back to Silo plugins"
                >
                  Back to Silo plugins
                </a>
                <Admin />
              </div>
            </main>
          }
        />
        <Route element={<Layout />}>
          <Route path="/" element={<Home />} />
          <Route path="/library" element={<Library />} />
          <Route path="/audiobook/:id" element={<Detail />} />
          <Route path="/series" element={<Series />} />
          <Route path="/series/:id" element={<SeriesDetail />} />
          <Route path="/authors" element={<Authors />} />
          <Route path="/authors/:id" element={<AuthorDetail />} />
          <Route path="/narrators" element={<Narrators />} />
          <Route path="/narrators/:id" element={<NarratorDetail />} />
          <Route path="/collections" element={<Collections />} />
          <Route path="/collections/:id" element={<CollectionDetail />} />
          <Route path="/smart-collections" element={<SmartCollections />} />
          <Route path="/smart-collections/:id" element={<SmartCollectionDetail />} />
          <Route path="/me/stats" element={<Stats />} />
          <Route path="/me/settings" element={<Settings />} />
          <Route path="/podcasts" element={<Podcasts />} />
          <Route path="/podcasts/:id" element={<PodcastDetail />} />
          <Route path="/apps" element={<Apps />} />
          <Route path="/me/requests" element={<MyRequests />} />
          <Route path="/admin/requests/:id" element={<Navigate to="/admin" replace />} />
          <Route path="/admin/settings" element={<Navigate to="/admin" replace />} />
          <Route path="/admin/sessions" element={<Navigate to="/admin" replace />} />
          <Route path="/admin/tokens" element={<Navigate to="/admin" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
      <Toaster />
    </PlaybackProvider>
  );
}
