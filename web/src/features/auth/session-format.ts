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

// Relative phrasing moved to `@/lib/relative-time` once the campaign sender pool
// needed it too — features may not import each other, so the one implementation
// lives in lib. Re-exported here so this module stays the auth screens' single
// formatting import.
export { relativeTime } from '@/lib/relative-time'

/** Absolute, locale-aware date+time for the "started" column. */
export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
}
