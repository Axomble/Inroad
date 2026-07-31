import { useSyncExternalStore } from 'react'
import { DARK_QUERY, prefersDark } from '@/lib/theme'

function subscribe(onChange: () => void): () => void {
  const media = globalThis.matchMedia?.(DARK_QUERY)
  media?.addEventListener('change', onChange)
  return () => media?.removeEventListener('change', onChange)
}

// jsdom and SSR have no meaningful OS preference; light is the safe default.
function getServerSnapshot(): boolean {
  return false
}

/** Subscribes to the OS colour-scheme preference. */
export function useSystemDark(): boolean {
  return useSyncExternalStore(subscribe, prefersDark, getServerSnapshot)
}
