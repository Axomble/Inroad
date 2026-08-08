import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { SectionBar } from '@/components/layout/page'
import { crmErrorMessage } from './error-copy'

/**
 * The chrome every CRM create form shares: a labelled section bar, a responsive
 * field grid, one alert surface for the server's reason, and one submit row.
 * Shared so a company, a pipeline and a deal are created through the same
 * affordance on whichever page they are created from.
 */
export function FormShell({
  title,
  onSubmit,
  busy,
  error,
  children,
}: {
  title: string
  onSubmit: React.FormEventHandler<HTMLFormElement>
  busy: boolean
  /** The RTK error itself, not a boolean — the server's reason is the message. */
  error: unknown
  children: React.ReactNode
}) {
  return (
    <form onSubmit={onSubmit} noValidate className="border-b border-border bg-surface/50">
      <SectionBar label={title} />
      <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">{children}</div>
      {error !== undefined && (
        <p role="alert" className="mx-5 mb-3 text-xs text-danger">
          {crmErrorMessage(error, 'The record could not be saved. Review the fields and try again.')}
        </p>
      )}
      <div className="flex justify-end border-t border-border px-5 py-3">
        <Button type="submit" variant="primary" size="sm" disabled={busy}>
          {busy && <Loader2 className="animate-spin" aria-hidden="true" />}
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

export function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string
  label: string
  hint?: string
  error?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-2">
        <Label htmlFor={id}>{label}</Label>
        {hint && <span className="text-[10px] text-muted-foreground">{hint}</span>}
      </div>
      {children}
      {error && <span role="alert" className="text-xs text-danger">{error}</span>}
    </div>
  )
}
