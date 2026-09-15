import type { PulseAttentionItem, PulseSeverity } from '@/features/pulse/api'

/**
 * Shared vocabulary for `pulse.attention[]` rows — used by the sidebar's
 * pulse card and the Overview page's "Needs attention" panel, so an operator
 * reads the same words for the same condition wherever it surfaces.
 *
 * Lives beside its consumers in `components/layout/` rather than inside
 * `pulse-card.tsx` so a feature can import it without pulling in chrome UI
 * (and without adding a non-component export to a component file).
 */

const SEVERITY_ORDER: Record<PulseSeverity, number> = { danger: 0, warn: 1, info: 2 }

/** Visually-hidden severity word paired with each row's color/shape cue. */
export const SEVERITY_SR: Record<PulseSeverity, string> = {
  danger: 'Critical:',
  warn: 'Warning:',
  info: 'Notice:',
}

/** Worst-first, without mutating the payload RTK Query froze. */
export function sortAttention(items: PulseAttentionItem[]): PulseAttentionItem[] {
  return [...items].sort((a, b) => SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity])
}

/**
 * Copy for known attention kinds; unknown kinds (a newer server) fall back to
 * the humanized identifier so new producers render without a frontend change.
 */
// Keys mirror the server's kind constants (internal/app/pulse/service.go);
// pulse-card.test.tsx asserts every known kind maps here so a renamed
// producer can't silently fall through to the humanized fallback again.
// Plain words over protocol names: "slowed down", "needs a DNS fix" — the
// technical detail (which check failed, which gate fired) rides along in the
// server-written `reason` line under each row.
const ATTENTION_LABELS: Record<string, (count: number) => string> = {
  mailbox_error: (n) => (n === 1 ? 'mailbox needs attention' : 'mailboxes need attention'),
  senders_gated: (n) => (n === 1 ? 'sender slowed down' : 'senders slowed down'),
  dmarc_failing: (n) => (n === 1 ? 'domain needs a DNS fix' : 'domains need a DNS fix'),
  cap_consumed: (n) => (n === 1 ? 'sender group near its daily limit' : 'sender groups near their daily limit'),
}

export function attentionLabel(kind: string, count: number): string {
  const label = ATTENTION_LABELS[kind]
  return label ? label(count) : kind.replace(/_/g, ' ')
}

/**
 * Server hrefs may carry a query string (`/app/mailboxes?status=error`);
 * TanStack's Link wants the search separated from the path.
 */
export function linkProps(href: string): { to: string; search?: Record<string, string> } {
  const queryStart = href.indexOf('?')
  if (queryStart === -1) return { to: href }
  return {
    to: href.slice(0, queryStart),
    search: Object.fromEntries(new URLSearchParams(href.slice(queryStart + 1))),
  }
}
