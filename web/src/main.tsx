import { StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider } from 'react-redux'
import { PersistGate } from 'redux-persist/integration/react'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { store, persistor } from './store'
import { routeTree } from './routeTree.gen'
import { PageSkeleton } from './components/shared/page-skeleton'
import { RouteError } from './components/shared/route-error'
import { NotFound } from './components/shared/not-found'
import { ErrorBoundary } from './components/error-boundary'
import './styles/globals.css'

// The store is injected into router context so route guards (see routes/app.tsx)
// can read auth state without importing the store singleton directly — that
// keeps route modules testable and the direction of dependency clean.
const router = createRouter({
  routeTree,
  context: { store },
  defaultPendingComponent: PageSkeleton,
  defaultErrorComponent: RouteError,
  defaultNotFoundComponent: NotFound,
})
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <Provider store={store}>
        <PersistGate loading={null} persistor={persistor}>
          <Suspense fallback={<PageSkeleton />}>
            <RouterProvider router={router} />
          </Suspense>
        </PersistGate>
      </Provider>
    </ErrorBoundary>
  </StrictMode>,
)
