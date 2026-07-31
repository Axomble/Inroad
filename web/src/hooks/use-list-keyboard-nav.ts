import { useCallback, useEffect, useRef, useState } from 'react'
import { useHotkey } from './use-hotkey'

export interface ListKeyboardNav {
  /** Index of the row the keyboard is on, or -1 when nothing is focused yet. */
  activeIndex: number
  /** True for the row at `activeIndex` — drives the row's highlight class. */
  isActive: (index: number) => boolean
  /**
   * Attach to each row so mouse and keyboard agree on what "current" means:
   * hovering a row moves the keyboard cursor there, so pressing Enter opens
   * what the user is looking at rather than a stale selection elsewhere.
   */
  onRowHover: (index: number) => void
  /** Ref for the scroll container, so the active row can be scrolled into view. */
  containerRef: React.RefObject<HTMLDivElement | null>
  reset: () => void
}

/**
 * `j`/`k` (and arrow key) navigation over a list, with Enter to open and Escape
 * to clear.
 *
 * Deliberately index-based rather than DOM-focus-based: rows here are `<li>`
 * elements with a click handler, not natively focusable controls, and making
 * every row a tab stop would wreck sequential tab order for keyboard users who
 * just want to reach the page's actions. The active row is exposed as state so
 * it can be styled, and `aria-activedescendant` is left to the caller when a row
 * is a real option.
 *
 * All bindings go through `useHotkey`, which already refuses to fire while the
 * user is typing in a field or inside a Radix menu/dialog.
 */
export function useListKeyboardNav({
  count,
  onOpen,
  enabled = true,
}: {
  count: number
  onOpen?: (index: number) => void
  enabled?: boolean
}): ListKeyboardNav {
  const [activeIndex, setActiveIndex] = useState(-1)
  const containerRef = useRef<HTMLDivElement | null>(null)

  // A shrinking list (a filter narrowed it) must not leave the cursor pointing
  // past the end, which would make Enter a no-op with a highlight still shown.
  useEffect(() => {
    setActiveIndex((current) => (current >= count ? count - 1 : current))
  }, [count])

  const move = useCallback(
    (delta: number) => {
      if (count === 0) return
      setActiveIndex((current) => {
        // First keypress enters the list at the near end rather than jumping to
        // the middle: `j` starts at the top, `k` starts at the bottom.
        if (current === -1) return delta > 0 ? 0 : count - 1
        const next = current + delta
        if (next < 0) return 0
        if (next > count - 1) return count - 1
        return next
      })
    },
    [count],
  )

  useHotkey({ key: 'j' }, () => move(1), enabled)
  useHotkey({ key: 'k' }, () => move(-1), enabled)
  useHotkey({ key: 'ArrowDown' }, () => move(1), enabled)
  useHotkey({ key: 'ArrowUp' }, () => move(-1), enabled)
  useHotkey({ key: 'Escape' }, () => setActiveIndex(-1), enabled)
  useHotkey(
    { key: 'Enter' },
    () => {
      if (activeIndex >= 0 && activeIndex < count) onOpen?.(activeIndex)
    },
    enabled && !!onOpen,
  )

  // Keep the cursor visible when it walks off the bottom of the scroll port.
  useEffect(() => {
    if (activeIndex < 0) return
    const container = containerRef.current
    const row = container?.querySelector<HTMLElement>(`[data-row-index="${activeIndex}"]`)
    row?.scrollIntoView({ block: 'nearest' })
  }, [activeIndex])

  const isActive = useCallback((index: number) => index === activeIndex, [activeIndex])
  const onRowHover = useCallback((index: number) => setActiveIndex(index), [])
  const reset = useCallback(() => setActiveIndex(-1), [])

  return { activeIndex, isActive, onRowHover, containerRef, reset }
}

const HINT_MOVE = { keys: 'j / k', label: 'move' } as const
const HINT_OPEN = { keys: '↵', label: 'open' } as const
const HINT_SEARCH = { keys: '/', label: 'search' } as const
const HINT_COMMANDS = { keys: '⌘K', label: 'commands' } as const

/** Hints for a list whose rows open something (pass `onOpen`). */
export const LIST_NAV_HINTS = [HINT_MOVE, HINT_OPEN, HINT_SEARCH, HINT_COMMANDS] as const

/**
 * Hints for a list whose rows have no detail view — advertising `↵ open` on a
 * list where Enter does nothing is worse than advertising nothing.
 */
export const LIST_NAV_HINTS_NO_OPEN = [HINT_MOVE, HINT_SEARCH, HINT_COMMANDS] as const
