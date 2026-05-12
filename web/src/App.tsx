import { Routes, Route, Navigate } from 'react-router';
import Layout from '@/components/Layout';
import Home from '@/pages/Home';
import Detail from '@/pages/Detail';
import Series from '@/pages/Series';
import SeriesDetail from '@/pages/SeriesDetail';
import Authors from '@/pages/Authors';
import AuthorDetail from '@/pages/AuthorDetail';
import Narrators from '@/pages/Narrators';
import NarratorDetail from '@/pages/NarratorDetail';
import Collections from '@/pages/Collections';
import CollectionDetail from '@/pages/CollectionDetail';
import Apps from '@/pages/Apps';
import MyRequests from '@/pages/MyRequests';
import AdminRequests from '@/pages/admin/Requests';
import AdminRequestDetail from '@/pages/admin/RequestDetail';
import AdminSettings from '@/pages/admin/Settings';
import AdminSessions from '@/pages/admin/Sessions';
import AdminTokens from '@/pages/admin/Tokens';
import { Toaster } from '@/components/ui/sonner';

export default function App() {
  return (
    <>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<Home />} />
          <Route path="/audiobook/:id" element={<Detail />} />
          <Route path="/series" element={<Series />} />
          <Route path="/series/:id" element={<SeriesDetail />} />
          <Route path="/authors" element={<Authors />} />
          <Route path="/authors/:id" element={<AuthorDetail />} />
          <Route path="/narrators" element={<Narrators />} />
          <Route path="/narrators/:id" element={<NarratorDetail />} />
          <Route path="/collections" element={<Collections />} />
          <Route path="/collections/:id" element={<CollectionDetail />} />
          <Route path="/apps" element={<Apps />} />
          <Route path="/me/requests" element={<MyRequests />} />
          <Route path="/admin" element={<AdminRequests />} />
          <Route path="/admin/requests/:id" element={<AdminRequestDetail />} />
          <Route path="/admin/settings" element={<AdminSettings />} />
          <Route path="/admin/sessions" element={<AdminSessions />} />
          <Route path="/admin/tokens" element={<AdminTokens />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
      <Toaster />
    </>
  );
}
