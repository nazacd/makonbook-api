import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, Navigate } from 'react-router';
import { RouterProvider } from 'react-router/dom';

import { ApiError } from './api/client';
import { meKey } from './api/hooks';
import { Home, Shell } from './components/Shell';
import { AccessPage } from './pages/Access';
import { CorrectionsPage } from './pages/Corrections';
import { IssuesPage } from './pages/Issues';
import { LessonsPage } from './pages/Lessons';
import { SessionPage } from './pages/Session';
import { WritesPage } from './pages/Writes';
import { PracticeQuestionPage } from './practice/QuestionPage';
import { PracticeQueuePage } from './practice/QueuePage';
import { PracticeTestPage } from './practice/TestPage';
import { PracticeTestsPage } from './practice/TestsPage';
import './styles.css';

// Any 401 means the session ended (signed out elsewhere, access removed,
// 12 hours passed): back to the sign-in screen.
function onError(error: unknown) {
  if (error instanceof ApiError && error.status === 401) {
    queryClient.setQueryData(meKey, null);
  }
}

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: {
      retry: (count, error) => !(error instanceof ApiError && error.status > 0 && error.status < 500) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

const router = createBrowserRouter([
  {
    element: <Shell />,
    children: [
      { index: true, element: <Home /> },
      { path: 'lessons', element: <LessonsPage /> },
      { path: 'sessions/:id', element: <SessionPage /> },
      { path: 'corrections', element: <CorrectionsPage /> },
      { path: 'writes', element: <WritesPage /> },
      { path: 'issues', element: <IssuesPage /> },
      { path: 'data-quality', element: <Navigate to="/issues" replace /> },
      { path: 'access', element: <AccessPage /> },
      { path: 'practice', element: <PracticeTestsPage /> },
      { path: 'practice/queue', element: <PracticeQueuePage /> },
      { path: 'practice/tests/:code', element: <PracticeTestPage /> },
      { path: 'practice/questions/:id', element: <PracticeQuestionPage /> },
      { path: '*', element: <Home /> },
    ],
  },
]);

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
