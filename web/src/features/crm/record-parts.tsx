import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { EmptyBlock, Page, PageBody } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import { crmErrorMessage } from './error-copy'

/**
 * The pieces a CRM record page is built from. Contacts, companies and deals are
 * all hubs onto the same related-record graph, so they share one panel, one
 * loading line, one empty line and one alert surface rather than each screen
 * re-inventing them slightly differently.
 */

/** A record page still fetching the record it is named after. */
export function RecordPageSkeleton({ label }: { label: string }) {
  return (
    <Page>
      <PageBody>
        <div className="m-4 h-72 animate-pulse rounded-xl bg-surface-2 sm:m-5" aria-label={label} />
      </PageBody>
    </Page>
  )
}

/**
 * A whole record page replaced by one statement — the record is missing, or the
 * request for it failed. Those are different facts, so the caller supplies the
 * wording and the way out; this only owns the layout.
 */
export function RecordPageMessage({
  title,
  description,
  action,
}: {
  title: string
  description: string
  action?: React.ReactNode
}) {
  return (
    <Page>
      <PageBody>
        <EmptyBlock title={title} description={description} action={action} />
      </PageBody>
    </Page>
  )
}

/** A titled panel; its `<h2>` is what makes the region findable in the outline. */
export function Section({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description?: string
  actions?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="min-w-0 rounded-xl border border-border bg-surface p-4 sm:p-5">
      <header className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold">{title}</h2>
          {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </header>
      {children}
    </section>
  )
}

/** One field of a record, inside a `<dl>`. */
export function Detail({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 break-words font-medium">{value}</dd>
    </div>
  )
}

export function InlineLoading({ label = 'Loading' }: { label?: string }) {
  return (
    <p className="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
      {label}
    </p>
  )
}

/** "Nothing here yet" — distinct from a failure, which gets `QueryErrorBanner`. */
export function MutedEmpty({ text }: { text: string }) {
  return <p className="rounded-lg border border-dashed border-border p-4 text-sm text-muted-foreground">{text}</p>
}

/** The page cap was reached — say so rather than let the list read as whole. */
export function MoreExist({ noun }: { noun: string }) {
  return <p role="status" className="pt-1 text-xs text-muted-foreground">Older {noun} are not shown.</p>
}

/**
 * Says plainly that a list stops short of the whole workspace. Paging the
 * remainder in needs an accumulating cache (RTK's `infiniteQuery`, which the
 * OpenAPI codegen does not emit yet); until then the honest thing is to tell the
 * user the list is partial rather than let it read as complete.
 */
export function TruncationNotice({ noun, shown }: { noun: string; shown: number }) {
  return (
    <p role="status" className="border-t border-border px-5 py-3 text-xs text-muted-foreground">
      Showing the first {shown} {noun}. More exist in this workspace and are not listed here yet.
    </p>
  )
}

/** The one alert surface for a failed CRM read, with the retry the user needs. */
export function QueryErrorBanner({
  error,
  fallback,
  onRetry,
  retrying = false,
  className = 'm-5',
}: {
  error: unknown
  fallback: string
  onRetry?: () => void
  retrying?: boolean
  className?: string
}) {
  return (
    <div
      role="alert"
      className={cn(
        'flex flex-wrap items-center gap-3 rounded-md border border-danger/30 bg-danger/10 p-4 text-sm text-danger',
        className,
      )}
    >
      <span className="min-w-0 flex-1">{crmErrorMessage(error, fallback)}</span>
      {onRetry && (
        <Button size="sm" onClick={onRetry} disabled={retrying}>
          {retrying ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          Try again
        </Button>
      )}
    </div>
  )
}
