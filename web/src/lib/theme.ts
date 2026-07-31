import { PERSIST_STORAGE_KEY } from '@/store/persist-key'
import type { ThemePreference } from '@/store/slices/ui'

export const DARK_QUERY = '(prefers-color-scheme: dark)'

/** Minimal store shape this module needs, so `lib/` never imports the store. */
interface ThemeStore {
  getState: () => { ui: { theme: ThemePreference } }
  subscribe: (listener: () => void) => () => void
}

export function prefersDark(): boolean {
  return globalThis.matchMedia?.(DARK_QUERY).matches ?? false
}

/**
 * Resolves a preference to a concrete appearance. Pure — the caller supplies the
 * system value, so React consumers can pass a subscribed value and re-render on
 * an OS-level change instead of reading a snapshot that silently goes stale.
 */
export function resolveDark(preference: ThemePreference, systemDark: boolean): boolean {
  return preference === 'system' ? systemDark : preference === 'dark'
}

export function applyTheme(preference: ThemePreference): void {
  document.documentElement.classList.toggle('dark', resolveDark(preference, prefersDark()))
}

/**
 * Reads the persisted preference synchronously, before React mounts.
 *
 * redux-persist rehydrates asynchronously, so waiting for the store would paint
 * the first frame in the wrong theme. This parses redux-persist's blob shape
 * directly — an object of slice name -> JSON string. Any absent, corrupt, or
 * unrecognised value is not an error worth propagating from app boot: the
 * documented fallback is 'system', which is also the slice's initial state.
 */
export function readPersistedTheme(): ThemePreference {
  try {
    const raw = globalThis.localStorage?.getItem(PERSIST_STORAGE_KEY)
    if (!raw) return 'system'
    const slices = JSON.parse(raw) as Record<string, string | undefined>
    if (!slices.ui) return 'system'
    const { theme } = JSON.parse(slices.ui) as { theme?: unknown }
    return theme === 'light' || theme === 'dark' ? theme : 'system'
  } catch {
    return 'system'
  }
}

/**
 * Keeps `<html class="dark">` in step with the persisted preference, and with
 * the OS preference while that preference is 'system'.
 *
 * The OS listener checks the current preference on every change instead of
 * applying `event.matches` blindly — otherwise an OS-level flip would silently
 * overwrite an explicit user choice. Returns a teardown for tests.
 *
 * Deliberately applies nothing up front: it is called in the same synchronous
 * tick as the pre-paint `applyTheme(readPersistedTheme())`, when redux-persist
 * has not rehydrated and the store still holds initial state — applying it there
 * would undo the pre-paint value and flash the wrong theme. redux-persist always
 * dispatches REHYDRATE, so the store's value lands on the next notification.
 * Painting the first frame is the caller's job; this keeps it in step after.
 */
export function startThemeSync(store: ThemeStore): () => void {
  const sync = () => applyTheme(store.getState().ui.theme)
  const unsubscribe = store.subscribe(sync)
  const media = globalThis.matchMedia?.(DARK_QUERY)
  media?.addEventListener('change', sync)
  return () => {
    unsubscribe()
    media?.removeEventListener('change', sync)
  }
}
