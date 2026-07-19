import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

/**
 * Encodes operational state in form + color, not color alone — a dot plus an
 * uppercase mono label so state reads at a glance and stays legible for
 * colorblind users. `warming` carries the reserved amber "heat" pulse.
 */
const pillVariants = cva('font-mono text-[10.5px] font-medium uppercase tracking-[0.1em]', {
  variants: {
    tone: {
      running: 'text-ok',
      warming: 'text-warm',
      paused: 'text-warn',
      draft: 'text-faint',
      failing: 'text-danger',
      done: 'text-muted-foreground',
    },
  },
  defaultVariants: { tone: 'draft' },
})

const dotColor: Record<NonNullable<VariantProps<typeof pillVariants>['tone']>, string> = {
  running: 'bg-ok',
  warming: 'bg-warm',
  paused: 'bg-warn',
  draft: 'bg-faint',
  failing: 'bg-danger',
  done: 'bg-muted-foreground',
}

export interface StatusPillProps extends VariantProps<typeof pillVariants> {
  children: React.ReactNode
  className?: string
  /** Show the leading status dot. */
  dot?: boolean
}

export function StatusPill({ tone = 'draft', dot = true, className, children }: StatusPillProps) {
  const t = tone ?? 'draft'
  return (
    <span data-slot="status-pill" className={cn('inline-flex items-center gap-1.5', className)}>
      {dot && (
        <span
          className={cn('size-1.5 rounded-full', dotColor[t], t === 'warming' && 'warm-pulse')}
          aria-hidden="true"
        />
      )}
      <span className={pillVariants({ tone })}>{children}</span>
    </span>
  )
}
