import { cn } from '@/lib/utils'

/**
 * Page scaffold — the shared vocabulary every screen composes from, so the app
 * has one rhythm: dense, edge-to-edge, hairline dividers instead of gutters and
 * cards. Small tracked-uppercase eyebrows replace large titles.
 */

type DivProps = React.HTMLAttributes<HTMLDivElement>

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

export function PageBody({ className, ...props }: DivProps) {
  return <div data-slot="page-body" className={cn('flex-1 overflow-y-auto', className)} {...props} />
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
