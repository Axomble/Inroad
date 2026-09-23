import { useId, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { ListHeader, ListHeaderCell } from '@/components/layout/page'
import { formatDateTime, formatShortDateTime } from '@/lib/datetime'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { AuditEvent } from '@/store/api'
import { actorCopy, metadataEntries, targetCopy } from './audit-log-copy'
import { actionLabel } from './audit-vocabulary'

// Column widths, shared by the header and every row so the two stay aligned.
// The target rides under the action rather than in a column of its own: beside
// two settings rails a fifth column squeezed the actor — the field an
// investigation reads first — down to a few truncated characters.
const COL_TIME = 'w-28 shrink-0'
const COL_ACTION = 'w-48 shrink-0'
const COL_ACTOR = 'min-w-0 flex-1'
const COL_IP = 'hidden w-32 shrink-0 md:block'

export function AuditEventHeader() {
  return (
    <ListHeader className="px-4 sm:px-5">
      {/* The chevron column, so the labels sit over the values rather than over it. */}
      <span className="w-3.5 shrink-0" aria-hidden="true" />
      <ListHeaderCell className={COL_TIME}>When</ListHeaderCell>
      <ListHeaderCell className={COL_ACTION}>Action · target</ListHeaderCell>
      <ListHeaderCell className={COL_ACTOR}>Actor</ListHeaderCell>
      <ListHeaderCell className={COL_IP}>IP</ListHeaderCell>
    </ListHeader>
  )
}

/**
 * One audit event: a dense summary line that expands to everything recorded.
 *
 * SECURITY: the actor email, target id, IP, user agent and every metadata value
 * can be attacker-chosen — a failed sign-in records whatever was typed into the
 * form. All of it is rendered as React text children and nothing else: never as
 * HTML, never as markdown, never turned into a link.
 */
export function AuditEventRow({ event }: { event: AuditEvent }) {
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const actor = actorCopy(event)
  const target = targetCopy(event)
  // A failed sign-in is the row an investigation is usually looking for, so it is
  // marked — with a dot AND the words "Sign-in failed", never the colour alone.
  const failed = event.action === 'auth.login_failed'

  return (
    <li data-slot="audit-event" className="border-b border-border">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        aria-controls={detailsId}
        className={cn(
          'flex w-full items-start gap-4 px-4 py-2 text-left text-[13px] transition-colors sm:px-5',
          'hover:bg-surface-2/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary',
          open && 'bg-surface-2/40',
        )}
      >
        <ChevronRight
          className={cn('mt-0.5 size-3.5 shrink-0 text-faint transition-transform', open && 'rotate-90')}
          aria-hidden="true"
        />
        <span className={cn(COL_TIME, 'flex flex-col')}>
          <time dateTime={event.created_at} title={formatDateTime(event.created_at)} className="text-foreground">
            {relativeTime(event.created_at)}
          </time>
          <span className="font-mono text-[11px] text-faint">{formatShortDateTime(event.created_at)}</span>
        </span>
        <span className={cn(COL_ACTION, 'flex min-w-0 flex-col')}>
          <span className="flex items-center gap-1.5">
            {failed && <span className="size-1.5 shrink-0 rounded-full bg-danger" aria-hidden="true" />}
            <span className={cn('truncate', failed ? 'text-danger' : 'text-foreground')}>
              {actionLabel(event.action)}
            </span>
          </span>
          <span data-slot="audit-target" className="truncate font-mono text-[11px] text-muted-foreground">
            {target ?? '—'}
          </span>
        </span>
        <span className={cn(COL_ACTOR, 'flex flex-col')}>
          <span data-slot="audit-actor" className="truncate text-foreground">
            {actor.primary}
          </span>
          {actor.secondary && <span className="truncate text-[12px] text-muted-foreground">{actor.secondary}</span>}
        </span>
        <span className={cn(COL_IP, 'truncate font-mono text-[12px] text-muted-foreground')}>{event.ip ?? '—'}</span>
      </button>

      {open && <AuditEventDetails id={detailsId} event={event} />}
    </li>
  )
}

function AuditEventDetails({ id, event }: { id: string; event: AuditEvent }) {
  const metadata = metadataEntries(event.metadata)

  return (
    <div
      id={id}
      data-slot="audit-event-details"
      className="grid gap-4 border-t border-border bg-surface/60 px-4 py-3 text-[12px] sm:px-5 sm:pl-[3.25rem] md:grid-cols-2"
    >
      <section>
        <h3 className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Details</h3>
        {metadata.length === 0 ? (
          <p className="text-muted-foreground">No additional details were recorded.</p>
        ) : (
          <DetailList rows={metadata} />
        )}
      </section>
      <section>
        <h3 className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Record</h3>
        <DetailList
          rows={[
            ['time', formatDateTime(event.created_at)],
            ['ip', event.ip ?? '—'],
            ['user agent', event.user_agent ?? '—'],
            ['action', event.action],
            ['actor', event.actor_id ?? '—'],
            ['on behalf of', event.actor_user_id ?? '—'],
            ['target', targetCopy(event) ?? '—'],
            ['event id', event.id],
          ]}
        />
      </section>
    </div>
  )
}

function DetailList({ rows }: { rows: readonly (readonly [string, string])[] }) {
  return (
    <dl className="grid grid-cols-[minmax(6rem,max-content)_1fr] gap-x-3 gap-y-1">
      {rows.map(([key, value]) => (
        <div key={key} className="contents">
          <dt className="font-mono text-faint">{key}</dt>
          {/* break-words: a user agent or pasted value has no spaces to wrap on. */}
          <dd className="min-w-0 break-words text-foreground [overflow-wrap:anywhere]">{value}</dd>
        </div>
      ))}
    </dl>
  )
}
