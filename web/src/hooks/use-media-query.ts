import { useCallback, useSyncExternalStore } from 'react'

/**
 * Whether a CSS media query currently matches, kept in sync as the viewport
 * changes.
 *
 * `useSyncExternalStore` rather than `useState` + an effect (the same choice
 * `useSystemDark` makes over the colour-scheme query): the browser already
 * holds this state, so subscribing to it directly avoids both the tearing a
 * mid-render resize would cause and the extra render an effect-based sync
 * costs on mount.
 *
 * Returns `false` where `matchMedia` is unavailable (jsdom without a stub,
 * SSR). For a `min-width` query that means the layout starts at its narrow
 * form and widens once the real value arrives — the safe direction, since the
 * narrow layout is the one that must never depend on the extra space.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const media = globalThis.matchMedia?.(query)
      media?.addEventListener('change', onChange)
      return () => media?.removeEventListener('change', onChange)
    },
    [query],
  )

  const getSnapshot = useCallback(() => globalThis.matchMedia?.(query).matches ?? false, [query])

  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)
}

function getServerSnapshot(): boolean {
  return false
}
