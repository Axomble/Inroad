/**
 * redux-persist's storage key. Lives alone so the store config and the
 * pre-paint theme read (see `lib/theme.ts`) cannot drift apart — the latter
 * parses this blob directly, before the store exists.
 */
export const PERSIST_KEY = 'inroad'

/** The localStorage key redux-persist actually writes, i.e. `persist:<key>`. */
export const PERSIST_STORAGE_KEY = `persist:${PERSIST_KEY}`
