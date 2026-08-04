import { AlertCircle, CheckCircle2 } from 'lucide-react'

export type Notice = { tone: 'ok' | 'error'; text: string }

/**
 * One page-level outcome banner: mutations report success or failure here
 * instead of each section growing its own alert markup. Rendered directly
 * under the topbar so it's never hidden beneath a dialog or a fold.
 */
export function NoticeBanner({ notice }: { notice: Notice }) {
  const isError = notice.tone === 'error'
  return (
    <div
      role={isError ? 'alert' : 'status'}
      className={
        isError
          ? 'flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-5 py-2.5 text-xs text-danger'
          : 'flex items-center gap-2 border-b border-ok/30 bg-ok/10 px-5 py-2.5 text-xs text-ok'
      }
    >
      {isError ? (
        <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
      ) : (
        <CheckCircle2 className="size-3.5 shrink-0" aria-hidden="true" />
      )}
      {notice.text}
    </div>
  )
}
