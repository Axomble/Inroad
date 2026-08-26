import { cn } from '@/lib/utils'
import type { InboxLabel } from './api'

/**
 * One operator-assigned label, as a chip.
 *
 * The colour is applied as a tinted background with a solid dot rather than as
 * the text colour: an arbitrary user-chosen hex has no guaranteed contrast
 * against the surface, so the readable part (the name) keeps the theme's own
 * foreground and the colour appears only where contrast doesn't matter.
 */
export function LabelChip({ label, className }: { label: InboxLabel; className?: string }) {
  return (
    <span
      className={cn(
        'inline-flex max-w-[10rem] items-center gap-1 rounded-md border border-border bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium text-foreground',
        className,
      )}
    >
      <span
        className="size-1.5 shrink-0 rounded-full"
        style={{ backgroundColor: label.color }}
        aria-hidden="true"
      />
      <span className="truncate">{label.name}</span>
    </span>
  )
}

/**
 * A thread's labels, as a compact run of chips.
 *
 * `max` caps how many render inline — a thread filed under a dozen labels
 * would otherwise crowd out the subject in a dense list row. The remainder is
 * summarised as "+N", and the full set is in the row's title attribute so it
 * is still discoverable without opening the thread.
 */
export function LabelChips({
  labels,
  max = 3,
  className,
}: {
  /**
   * Optional because the generated client types it so: the server always sends
   * `[]` for an unlabelled thread, but a field composed through OpenAPI's
   * `allOf` loses its `required` marker in codegen. Treating undefined as "no
   * labels" is the honest reading either way.
   */
  labels: readonly InboxLabel[] | undefined
  max?: number
  className?: string
}) {
  if (!labels || labels.length === 0) return null
  const shown = labels.slice(0, max)
  const hidden = labels.length - shown.length

  return (
    <span className={cn('inline-flex items-center gap-1', className)} title={labels.map((l) => l.name).join(', ')}>
      {shown.map((label) => (
        <LabelChip key={label.id} label={label} />
      ))}
      {hidden > 0 && (
        <span className="shrink-0 font-mono text-[10px] text-faint">
          +{hidden}
          <span className="sr-only"> more labels</span>
        </span>
      )}
    </span>
  )
}
