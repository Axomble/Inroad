import { configureStore, combineReducers } from '@reduxjs/toolkit'
import { persistReducer, persistStore } from 'redux-persist'
import { storage } from './storage'
import { api } from './api'
import ui from './slices/ui'
import auth from './slices/auth'

const rootReducer = combineReducers({
  [api.reducerPath]: api.reducer,
  ui,
  auth,
})

// Persist the UI slice ONLY. The session lives in memory (restored from the
// httpOnly refresh cookie on boot, see features/auth/use-auth-bootstrap.ts) and
// the RTK Query `api` cache must never be persisted.
const persistConfig = { key: 'inroad', storage, whitelist: ['ui'] }
const persisted = persistReducer(persistConfig, rootReducer)

export const store = configureStore({
  reducer: persisted,
  middleware: (getDefault) =>
    getDefault({ serializableCheck: { ignoredActions: ['persist/PERSIST', 'persist/REHYDRATE'] } })
      .concat(api.middleware),
})
export const persistor = persistStore(store)

export type RootState = ReturnType<typeof store.getState>
export type AppDispatch = typeof store.dispatch
