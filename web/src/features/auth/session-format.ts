// Presentation helpers for the active-sessions screen. Pure functions, unit
// tested separately from the component — a session row is only ever as legible
// as these make the raw `user_agent` / timestamp strings.

/**
 * Best-effort friendly label for a session's raw `user_agent`, e.g.
 * "Chrome on Windows". The header is attacker-controlled free text, so this is
 * a hint for the user to recognise their own device — never a security signal.
 * Order matters: several engines masquerade as one another in the UA string.
 */
export function describeUserAgent(userAgent: string | null | undefined): string {
  if (!userAgent) return 'Unknown device'
  const browser = detectBrowser(userAgent)
  const os = detectOs(userAgent)
  if (browser && os) return `${browser} on ${os}`
  return browser ?? os ?? 'Unknown device'
}

function detectBrowser(ua: string): string | null {
  if (/\bEdg\//.test(ua)) return 'Edge'
  if (/\bOPR\/|\bOpera\b/.test(ua)) return 'Opera'
  if (/\bFirefox\//.test(ua)) return 'Firefox'
  // Chromium reports both "Chrome" and "Safari"; check it before Safari.
  if (/\bChrome\//.test(ua)) return 'Chrome'
  if (/\bSafari\//.test(ua)) return 'Safari'
  return null
}

function detectOs(ua: string): string | null {
  if (/\bWindows NT\b/.test(ua)) return 'Windows'
  if (/\biPhone\b|\biPad\b|\biPod\b/.test(ua)) return 'iOS'
  if (/\bAndroid\b/.test(ua)) return 'Android'
  if (/\bMac OS X\b|\bMacintosh\b/.test(ua)) return 'macOS'
  if (/\bLinux\b/.test(ua)) return 'Linux'
  return null
}

const UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000_000],
  ['month', 2_592_000_000],
  ['day', 86_400_000],
  ['hour', 3_600_000],
  ['minute', 60_000],
]

/**
 * Relative phrasing for an ISO timestamp, e.g. "in 5 days" / "3 hours ago".
 * `now` is injectable so tests are deterministic.
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const diffMs = new Date(iso).getTime() - now
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  const abs = Math.abs(diffMs)
  for (const [unit, ms] of UNITS) {
    if (abs >= ms) return rtf.format(Math.round(diffMs / ms), unit)
  }
  return rtf.format(Math.round(diffMs / 1000), 'second')
}

/** Absolute, locale-aware date+time for the "started" column. */
export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
}
