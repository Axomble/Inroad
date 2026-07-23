import { useEffect, useState } from 'react'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { AlertCircle, CheckCircle2, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { api } from '@/store/api'
import { useAppDispatch } from '@/store/hooks'

const routeApi = getRouteApi('/app/mailboxes')

// Plain, actionable copy per backend reason code. Kept exhaustive so an
// unmapped/unknown reason falls through to the generic message below.
const errorCopy: Record<string, string> = {
  denied: 'Google sign-in was cancelled.',
  bad_state: 'That connection link expired — start again.',
  already_connected: 'That mailbox is already connected.',
  disabled: "Gmail connect isn't configured on this server.",
  no_email: 'Could not read the mailbox address from Google.',
  exchange_failed: 'Could not complete the Google connection — try again.',
}

const GENERIC_ERROR = 'Could not complete the Google connection — try again.'

/**
 * Renders the Gmail OAuth callback outcome once the public
 * `/oauth/google/callback` handler redirects the browser back to
 * `/app/mailboxes?connected=<email>` or `?oauth_error=<reason>`.
 *
 * The params are captured on first render (a snapshot), then stripped from the
 * URL via a replace-navigation so a refresh or re-render can't re-trigger the
 * banner. On a successful connect the Mailbox LIST tag is invalidated so the
 * freshly connected row appears without a manual refetch.
 */
export function OauthCallbackBanner() {
  const search = routeApi.useSearch()
  const navigate = useNavigate()
  const dispatch = useAppDispatch()
  // Snapshot on mount: the effect below empties the live search a tick later,
  // so the banner must read from this frozen copy to stay visible.
  const [notice] = useState<{ connected?: string; error?: string }>(() => ({
    connected: search.connected,
    error: search.oauth_error,
  }))
  const [dismissed, setDismissed] = useState(false)

  useEffect(() => {
    if (!notice.connected && !notice.error) return
    // The new Gmail mailbox isn't in the cached list yet — refetch it.
    if (notice.connected) dispatch(api.util.invalidateTags([{ type: 'Mailbox', id: 'LIST' }]))
    // Strip ?connected / ?oauth_error so a refresh doesn't re-show this.
    void navigate({ to: '/app/mailboxes', search: {}, replace: true })
    // Runs once: `notice` is a first-render snapshot and never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (dismissed || (!notice.connected && !notice.error)) return null

  if (notice.connected) {
    return (
      <BannerShell tone="ok" onDismiss={() => setDismissed(true)}>
        <CheckCircle2 className="size-4 shrink-0 text-ok" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          Gmail mailbox <span className="font-medium text-foreground">{notice.connected}</span> connected.
        </span>
      </BannerShell>
    )
  }

  return (
    <BannerShell tone="danger" onDismiss={() => setDismissed(true)}>
      <AlertCircle className="size-4 shrink-0 text-danger" aria-hidden="true" />
      <span className="min-w-0 flex-1">{(notice.error && errorCopy[notice.error]) || GENERIC_ERROR}</span>
    </BannerShell>
  )
}

/**
 * Shared alert chrome (border + tinted background + dismiss button) for both
 * the OAuth callback banner and the Gmail "start" error banner on the
 * mailboxes page, so there is a single alert surface to style and reason about.
 * Callers supply the icon and message as `children`.
 */
export function BannerShell({
  tone,
  onDismiss,
  children,
}: {
  tone: 'ok' | 'danger'
  onDismiss: () => void
  children: React.ReactNode
}) {
  return (
    <div
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cn(
        'flex items-center gap-3 border-b px-5 py-2 text-[13px] text-foreground',
        tone === 'ok' ? 'border-ok/30 bg-ok/10' : 'border-danger/30 bg-danger/10',
      )}
    >
      {children}
      <Button variant="ghost" size="icon-sm" className="shrink-0" aria-label="Dismiss" onClick={onDismiss}>
        <X className="size-4" />
      </Button>
    </div>
  )
}
