import { AlertTriangle, X } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * The single error surface for the assistant panel. Every failed agent action
 * — send, stop, rename, archive, dequeue, a dropped stream — renders through
 * this one component so the panel never grows a second, subtly different
 * alert. The icon plus the word "problem" in the copy means the message never
 * relies on colour alone.
 */
export function AgentAlert({
  message,
  onDismiss,
  className,
}: {
  message: string
  onDismiss?: () => void
  className?: string
}) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-2 border-y border-danger/25 bg-danger/10 px-3 py-2 text-[11px] leading-4 text-danger',
        className,
      )}
    >
      <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">{message}</span>
      {onDismiss && (
        <button
          type="button"
          aria-label="Dismiss error"
          className="shrink-0 rounded p-0.5 hover:bg-danger/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-danger"
          onClick={onDismiss}
        >
          <X className="size-3.5" aria-hidden="true" />
        </button>
      )}
    </div>
  )
}
