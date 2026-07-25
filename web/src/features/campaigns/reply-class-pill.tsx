import { cn } from '@/lib/utils'
import type { CampaignEnrollment } from './api'

/**
 * The reply classes the backend stores on an enrollment
 * (`sequence_enrollments.reply_class`), derived from the generated API type so
 * this stays in lockstep with the backend enum — add a class there and the
 * `classMeta`/`dotColor` maps below fail to compile until they cover it.
 * `null`/absent means the contact hasn't replied (or the reply wasn't
 * classified) — the pill renders nothing then.
 */
export type ReplyClass = NonNullable<CampaignEnrollment['reply_class']>

/**
 * Per-class label + tone. The label is the primary signal so state stays
 * legible without color (colorblind-safe); the color is redundant reinforcement.
 * Tones reuse the shared palette tokens: positive replies read as ok/green,
 * negative and unsubscribe as danger, automated (OOO/auto-reply) as muted, and
 * neutral/unknown stay faint.
 */
const classMeta: Record<ReplyClass, { label: string; tone: string }> = {
  positive: { label: 'Positive', tone: 'text-ok' },
  negative: { label: 'Negative', tone: 'text-danger' },
  neutral: { label: 'Neutral', tone: 'text-faint' },
  out_of_office: { label: 'Out of office', tone: 'text-muted-foreground' },
  auto_reply: { label: 'Auto-reply', tone: 'text-muted-foreground' },
  unsubscribe: { label: 'Unsubscribed', tone: 'text-danger' },
  unknown: { label: 'Unknown', tone: 'text-faint' },
}

const dotColor: Record<ReplyClass, string> = {
  positive: 'bg-ok',
  negative: 'bg-danger',
  neutral: 'bg-faint',
  out_of_office: 'bg-muted-foreground',
  auto_reply: 'bg-muted-foreground',
  unsubscribe: 'bg-danger',
  unknown: 'bg-faint',
}

// Narrow an arbitrary backend string (the API field is a plain string) to a
// known ReplyClass; anything unexpected — including null/undefined — yields
// undefined so the pill renders nothing rather than an empty/garbage badge.
function toReplyClass(value: string | null | undefined): ReplyClass | undefined {
  return value != null && value in classMeta ? (value as ReplyClass) : undefined
}

export interface ReplyClassPillProps {
  /** The enrollment's stored reply class, or null/undefined when none. */
  replyClass: string | null | undefined
  className?: string
}

/**
 * ReplyClassPill renders a small pill for a contact's classified reply. It
 * returns null when there is no (recognized) class, so callers can render it
 * unconditionally in a row.
 */
export function ReplyClassPill({ replyClass, className }: ReplyClassPillProps) {
  const key = toReplyClass(replyClass)
  if (!key) return null
  const { label, tone } = classMeta[key]
  return (
    <span data-slot="reply-class-pill" className={cn('inline-flex items-center gap-1.5', className)}>
      <span className={cn('size-1.5 rounded-full', dotColor[key])} aria-hidden="true" />
      <span className={cn('font-mono text-[10.5px] font-medium uppercase tracking-[0.1em]', tone)}>{label}</span>
    </span>
  )
}
