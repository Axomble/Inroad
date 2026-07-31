import { cn } from '@/lib/utils'

/**
 * Page scaffold — the shared vocabulary every screen composes from, so the app
 * has one rhythm: dense, edge-to-edge, hairline dividers instead of gutters and
 * cards. Small tracked-uppercase eyebrows replace large titles.
 */

// `ref` as a plain prop (React 19), matching the convention `components/ui/input.tsx`
// already uses. `PageBody` needs it so a list can hand its scroll container to
// `useListKeyboardNav` and keep the active row in view.
type DivProps = React.HTMLAttributes<HTMLDivElement> & { ref?: React.Ref<HTMLDivElement> }

export function Page({ className, ...props }: DivProps) {
  return <div data-slot="page" className={cn('flex h-full flex-col', className)} {...props} />
}

export function PageTopbar({
  eyebrow,
  title,
  subtitle,
  actions,
  className,
}: {
  eyebrow: string
  title?: React.ReactNode
  subtitle?: React.ReactNode
  actions?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="page-topbar"
      className={cn(
        'sticky top-0 z-20 flex h-12 items-center gap-3 border-b border-border bg-surface px-5',
        className,
      )}
    >
      <span className="font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-faint">{eyebrow}</span>
      {title && <span className="text-sm font-semibold text-foreground">{title}</span>}
      {subtitle && <span className="truncate text-xs text-muted-foreground">{subtitle}</span>}
      {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
    </div>
  )
}

export function SectionBar({
  label,
  count,
  children,
  className,
}: {
  label: string
  count?: React.ReactNode
  children?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="section-bar"
      className={cn('flex h-10 items-center gap-2 border-b border-border px-5', className)}
    >
      <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">{label}</span>
      {count != null && <span className="font-mono text-[11px] tabular-nums text-muted-foreground">{count}</span>}
      {children && <div className="ml-auto flex items-center gap-2">{children}</div>}
    </div>
  )
}

export function StatStrip({ className, ...props }: DivProps) {
  return (
    <div
      data-slot="stat-strip"
      className={cn('grid grid-cols-2 border-b border-border md:grid-cols-4', className)}
      {...props}
    />
  )
}

export function Stat({
  label,
  value,
  sub,
  dot,
  className,
}: {
  label: React.ReactNode
  value: React.ReactNode
  sub?: React.ReactNode
  /** A leading status dot element (e.g. from StatusPill's palette). */
  dot?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="stat"
      className={cn('border-r border-border px-5 py-3.5 last:border-r-0 [&:nth-child(2)]:border-r-0 md:[&:nth-child(2)]:border-r', className)}
    >
      <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        {dot}
        {label}
      </div>
      <div className="mt-1 text-[27px] font-light leading-none tabular-nums text-foreground">{value}</div>
      {sub && <div className="mt-1 font-mono text-[11px] text-muted-foreground">{sub}</div>}
    </div>
  )
}

/**
 * Column labels for a list. Rows in this app are flex, not `<table>`, so the
 * header mirrors the row's own flex layout: give each cell the same width /
 * `flex-1` as the column it names. Without this, values like `60/day` and a
 * bare status word arrive unexplained on first view.
 *
 * `sticky` so the labels survive scrolling a long list.
 */
export function ListHeader({ className, ...props }: DivProps) {
  return (
    <div
      data-slot="list-header"
      role="row"
      className={cn(
        'sticky top-0 z-10 flex items-center gap-4 border-b border-border bg-surface-2/60 px-5 py-1.5',
        'font-mono text-[10px] uppercase tracking-[0.14em] text-faint backdrop-blur-sm',
        className,
      )}
      {...props}
    />
  )
}

/** One column label inside a `ListHeader`. `role="columnheader"` for a11y. */
export function ListHeaderCell({ className, ...props }: DivProps) {
  return <div role="columnheader" className={cn('shrink-0', className)} {...props} />
}

export function PageBody({ className, ...props }: DivProps) {
  return <div data-slot="page-body" className={cn('flex-1 overflow-y-auto', className)} {...props} />
}

/**
 * Footer strip of keyboard affordances. Discoverability is the whole point of
 * shortcuts — an unadvertised shortcut may as well not exist — so any list that
 * wires `useListKeyboardNav` should render this beneath it.
 */
export function HintBar({
  hints,
  children,
  className,
}: {
  hints: readonly { keys: string; label: string }[]
  children?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="hint-bar"
      className={cn(
        'flex h-8 shrink-0 items-center gap-3 border-t border-border px-5 text-[11px] text-faint',
        className,
      )}
    >
      {hints.map((h) => (
        <span key={h.keys} className="flex items-center gap-1.5">
          <kbd className="rounded border border-border bg-surface-2 px-1 font-mono text-[10px] text-muted-foreground">
            {h.keys}
          </kbd>
          {h.label}
        </span>
      ))}
      {children && <div className="ml-auto flex items-center gap-2">{children}</div>}
    </div>
  )
}

export function EmptyBlock({
  title,
  description,
  action,
  className,
}: {
  title: string
  description?: string
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div data-slot="empty-block" className={cn('px-5 py-20 text-center', className)}>
      <p className="text-sm font-medium text-foreground">{title}</p>
      {description && <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted-foreground">{description}</p>}
      {action && <div className="mt-5 flex justify-center">{action}</div>}
    </div>
  )
}
