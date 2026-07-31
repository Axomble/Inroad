import { useEffect, useRef } from 'react'

/** A keystroke to match, normalised so callers don't repeat modifier plumbing. */
export interface Hotkey {
  /** `event.key`, compared case-insensitively (so `k` matches `K`). */
  key: string
  /** Require Cmd (mac) or Ctrl (everywhere else). Never both-or-either by accident. */
  mod?: boolean
  shift?: boolean
  /**
   * Fire even while the user is typing in an input, textarea, select, or
   * contentEditable. Defaults to `false`: a bare `/` or `j` must never steal a
   * character from someone filling in a form.
   */
  whileTyping?: boolean
}

/** True when the event target is a text-entry surface we must not hijack. */
function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable ||
    // Radix menus/dialogs manage their own keyboard model; don't fight them.
    target.closest('[role="menu"],[role="dialog"],[role="listbox"]') !== null
  )
}

function matches(e: KeyboardEvent, hotkey: Hotkey): boolean {
  if (e.key.toLowerCase() !== hotkey.key.toLowerCase()) return false
  const mod = e.metaKey || e.ctrlKey
  if (!!hotkey.mod !== mod) return false
  if (!!hotkey.shift !== e.shiftKey) return false
  return true
}

/**
 * Bind a document-level keyboard shortcut for as long as the component is
 * mounted.
 *
 * The handler is held in a ref so an inline arrow function doesn't detach and
 * re-attach the listener on every render — callers get to pass `() => ...`
 * without wrapping it in `useCallback`, which is the mistake this hook exists
 * to make impossible.
 *
 * Pass `enabled: false` to bind nothing (e.g. a shortcut that only applies
 * while a panel is open) rather than conditionally calling the hook.
 */
export function useHotkey(hotkey: Hotkey, handler: (e: KeyboardEvent) => void, enabled = true): void {
  const handlerRef = useRef(handler)
  handlerRef.current = handler

  const { key, mod, shift, whileTyping } = hotkey

  useEffect(() => {
    if (!enabled) return
    function onKeyDown(e: KeyboardEvent) {
      if (e.defaultPrevented) return
      if (!whileTyping && isTypingTarget(e.target)) return
      if (!matches(e, { key, mod, shift })) return
      e.preventDefault()
      handlerRef.current(e)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [enabled, key, mod, shift, whileTyping])
}
