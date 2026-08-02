import { useEffect, useRef, useState } from 'react'

/**
 * A text input that echoes every keystroke immediately but only *commits* once
 * the user pauses.
 *
 * For anything that turns typing into work — a server-side search, a URL write —
 * the field must never wait on the commit to redraw, or it feels laggy, and the
 * commit must not fire per keystroke, or "acme" is four requests and four history
 * entries instead of one.
 *
 * `value` stays authoritative: when it changes from the outside (Back, a shared
 * link, a Clear button) the echo re-syncs to it, so the box can't drift from the
 * state it represents.
 */
export function useDebouncedInput(
  value: string,
  commit: (next: string) => void,
  delayMs = 300,
): readonly [string, (next: string) => void] {
  const [typed, setTyped] = useState(value)

  // Held in a ref so a caller's inline arrow doesn't restart the timer on every
  // render — the delay must measure the user's pause, not the render cadence.
  const commitRef = useRef(commit)
  useEffect(() => {
    commitRef.current = commit
  })

  useEffect(() => {
    setTyped(value)
  }, [value])

  useEffect(() => {
    if (typed === value) return
    const timer = setTimeout(() => commitRef.current(typed), delayMs)
    return () => clearTimeout(timer)
  }, [typed, value, delayMs])

  return [typed, setTyped] as const
}
