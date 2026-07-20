import { configureStore, combineReducers, type Store } from '@reduxjs/toolkit'
import { render, type RenderOptions } from '@testing-library/react'
import { Provider } from 'react-redux'
import type { ReactElement } from 'react'
import { emptyApi } from '@/store/empty-api'
import authReducer, { type AuthState } from '@/store/slices/auth'
import uiReducer from '@/store/slices/ui'

type PreloadedState = {
  auth?: Partial<AuthState>
}

function makeReducer() {
  return combineReducers({
    [emptyApi.reducerPath]: emptyApi.reducer,
    auth: authReducer,
    ui: uiReducer,
  })
}

export function makeTestStore(preloaded?: PreloadedState) {
  const rootReducer = makeReducer()
  const preloadedState = preloaded?.auth
    ? { auth: { ...rootReducer(undefined, { type: '@@INIT' }).auth, ...preloaded.auth } }
    : undefined
  return configureStore({
    reducer: rootReducer,
    preloadedState,
    middleware: (getDefault) => getDefault().concat(emptyApi.middleware),
  })
}

export type TestStore = ReturnType<typeof makeTestStore>

/**
 * Wraps `render()` from @testing-library/react with a fresh Redux store per
 * call — instead of leaking the production store singleton across tests,
 * which shares state (auth, api cache, subscriptions) between them and makes
 * failures order-dependent.
 */
export function renderWithProviders(
  ui: ReactElement,
  { preloadedState, store = makeTestStore(preloadedState), ...renderOptions }: {
    preloadedState?: PreloadedState
    store?: Store
  } & Omit<RenderOptions, 'wrapper'> = {},
) {
  function Wrapper({ children }: { children: React.ReactNode }) {
    return <Provider store={store}>{children}</Provider>
  }
  return { store, ...render(ui, { wrapper: Wrapper, ...renderOptions }) }
}
