import { configureStore, combineReducers } from '@reduxjs/toolkit'
import { setupListeners } from '@reduxjs/toolkit/query'
import { persistReducer, persistStore } from 'redux-persist'
import { storage } from './storage'
import { PERSIST_KEY } from './persist-key'
import { api } from './api'
import ui from './slices/ui'
import auth from './slices/auth'
import agent from './slices/agent'
import toast from './slices/toast'

const rootReducer = combineReducers({
  [api.reducerPath]: api.reducer,
  ui,
  auth,
  agent,
  toast,
})

// Persist the UI slice ONLY. The session lives in memory (restored from the
// httpOnly refresh cookie on boot, see features/auth/use-auth-bootstrap.ts),
// the RTK Query `api` cache must never be persisted, and `toast` holds
// transient notices that would be stale the moment they rehydrated.
// persist-whitelist.test.ts asserts this against what redux-persist actually
// writes to storage.
const persistConfig = { key: PERSIST_KEY, storage, whitelist: ['ui'] }
const persisted = persistReducer(persistConfig, rootReducer)

export const store = configureStore({
  reducer: persisted,
  middleware: (getDefault) =>
    getDefault({ serializableCheck: { ignoredActions: ['persist/PERSIST', 'persist/REHYDRATE'] } })
      .concat(api.middleware),
})
export const persistor = persistStore(store)

// Without this, RTK Query never learns about focus/visibility changes, so
// `skipPollingIfUnfocused` (the pulse poll) silently does nothing and
// background tabs keep polling forever. `refetchOnFocus`/`refetchOnReconnect`
// stay off — they're not set on createApi — so focus tracking for polling is
// the only behavior this enables.
setupListeners(store.dispatch)

export type RootState = ReturnType<typeof store.getState>
export type AppDispatch = typeof store.dispatch
