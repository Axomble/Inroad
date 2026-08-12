import { useEffect, useRef } from 'react'
import { Link } from '@tanstack/react-router'
import { AlertCircle, CheckCircle2, Info, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { dismissToast, type Toast, type ToastTone } from '@/store/slices/toast'

/**
 * Renders the toast stack. Mounted once, in the app shell.
 *
 * Auto-dismissal lives here rather than in the reducer: a timer is a side
 * effect, and a reducer that schedules one stops being a pure function of its
 * input. The slice only ever holds what's on screen.
 *
 * Errors do not auto-dismiss. A success can be missed harmlessly — the thing
 * succeeded — but a failure the user never read is a failure they think didn't
 * happen, so it stays until acknowledged.
 */
const DISMISS_AFTER_MS = 6000

const TONE_ICON = { ok: CheckCircle2, error: AlertCircle, info: Info } as const

const TONE_CLASS: Record<ToastTone, string> = {
  ok: 'border-ok/30 bg-ok/10 text-ok',
  error: 'border-danger/30 bg-danger/10 text-danger',
  info: 'border-border bg-surface text-foreground',
}

export function ToastHost() {
  const items = useAppSelector((state) => state.toast.items)

  return (
    // `pointer-events-none` on the container so the empty region below the
    // stack never swallows clicks meant for the page; each toast turns them
    // back on for itself.
    <div
      data-slot="toast-host"
      className="pointer-events-none fixed inset-x-0 bottom-0 z-50 flex flex-col items-center gap-2 p-4 sm:items-end sm:p-6"
    >
      {/* One live region for the whole stack. `polite` so a toast never
          interrupts what a screen reader is already saying; errors carry their
          severity in the visually-hidden prefix instead. */}
      <div aria-live="polite" aria-atomic="false" className="contents">
        {items.map((toast) => (
          <ToastRow key={toast.id} toast={toast} />
        ))}
      </div>
    </div>
  )
}

function ToastRow({ toast }: { toast: Toast }) {
  const dispatch = useAppDispatch()
  const Icon = TONE_ICON[toast.tone]
  // Read the id through a ref so the effect depends only on the id itself:
  // re-running it on a re-render would restart the countdown every time the
  // stack above this toast changed.
  const dismiss = useRef(() => dispatch(dismissToast(toast.id)))
  dismiss.current = () => dispatch(dismissToast(toast.id))

  useEffect(() => {
    if (toast.tone === 'error') return
    const timer = setTimeout(() => dismiss.current(), DISMISS_AFTER_MS)
    return () => clearTimeout(timer)
  }, [toast.tone])

  return (
    <div
      data-slot="toast"
      data-tone={toast.tone}
      className={cn(
        'pointer-events-auto flex w-full max-w-sm items-start gap-2.5 rounded-xl border px-3.5 py-3 text-[13px] shadow-lg backdrop-blur-sm',
        TONE_CLASS[toast.tone],
      )}
    >
      <Icon className="mt-px size-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        {toast.tone === 'error' && <span className="sr-only">Error: </span>}
        <span className="block">{toast.text}</span>
        {toast.href && (
          <Link
            to={toast.href}
            onClick={() => dispatch(dismissToast(toast.id))}
            className="mt-1 inline-block font-medium underline underline-offset-2 hover:no-underline"
          >
            {toast.hrefLabel ?? 'View'}
          </Link>
        )}
      </div>
      <button
        type="button"
        onClick={() => dispatch(dismissToast(toast.id))}
        aria-label="Dismiss notification"
        className="-mr-1 -mt-0.5 shrink-0 rounded p-1 opacity-60 transition-opacity hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-current"
      >
        <X className="size-3.5" />
      </button>
    </div>
  )
}
