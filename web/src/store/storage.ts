import type { WebStorage } from 'redux-persist'

/**
 * localStorage-backed storage for redux-persist.
 *
 * We define this explicitly instead of importing `redux-persist/lib/storage`
 * because that package's CJS `exports.default` interop resolves to the module
 * namespace under Vite/rolldown, leaving `storage.getItem` undefined — which
 * hangs PersistGate and blanks the app. This also degrades safely when
 * localStorage is unavailable (SSR, private-mode quirks).
 */
const noop: WebStorage = {
  getItem: () => Promise.resolve(null),
  setItem: () => Promise.resolve(),
  removeItem: () => Promise.resolve(),
}

function createStorage(): WebStorage {
  try {
    const ls = globalThis.localStorage
    if (!ls) return noop
    return {
      getItem: (key) => Promise.resolve(ls.getItem(key)),
      setItem: (key, value) => Promise.resolve(ls.setItem(key, value)),
      removeItem: (key) => Promise.resolve(ls.removeItem(key)),
    }
  } catch {
    return noop
  }
}

export const storage = createStorage()
