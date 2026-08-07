import { useEffect, useState } from 'react'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { AlertCircle, CheckCircle2, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { api } from '@/store/api'
import { useAppDispatch } from '@/store/hooks'
import { mailboxProviderLabel } from './provider'

const routeApi = getRouteApi('/app/mailboxes')

// Plain, actionable copy per backend reason code. Kept exhaustive so an
// unmapped/unknown reason falls through to the generic message below. `disabled`
// is provider-specific and built at render time from the callback's provider
// tag (see errorMessage), so it lives outside this static map.
const errorCopy: Record<string, string> = {
  denied: 'Sign-in was cancelled.',
  bad_state: 'That connection link expired — start again.',
  already_connected: 'That mailbox is already connected.',
  no_email: "Couldn't read the mailbox address from your account.",
  exchange_failed: "Couldn't complete the connection — try again.",
}

const GENERIC_ERROR = "Couldn't complete the connection — try again."

// Maps a backend reason code to banner copy. `disabled` is the one reason that
// names the provider, so it uses the callback's provider label; every other
// reason is looked up in the static map (falling back to the generic message).
function errorMessage(reason: string | undefined, label: string): string {
  if (reason === 'disabled') return `${label} connect isn't configured on this server.`
  return (reason && errorCopy[reason]) || GENERIC_ERROR
}

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
  // so the banner must read from this frozen copy to stay visible. `provider` is
  // the callback's &provider tag ('gmail'|'m365') used to pick the copy label.
  const [notice] = useState<{ connected?: string; error?: string; provider?: string }>(() => ({
    connected: search.connected,
    error: search.oauth_error,
    provider: search.provider,
  }))
  const [dismissed, setDismissed] = useState(false)
  // gmail→"Gmail", m365→"Microsoft 365"; an absent/unknown tag → "Mailbox".
  const providerLabel = mailboxProviderLabel(notice.provider) ?? 'Mailbox'

  useEffect(() => {
    if (!notice.connected && !notice.error) return
    // The new mailbox isn't in the cached list yet — refetch it.
    if (notice.connected) dispatch(api.util.invalidateTags([{ type: 'Mailbox', id: 'LIST' }]))
    // Strip ?connected / ?oauth_error / ?provider so a refresh doesn't re-show
    // this. Clear those three keys specifically rather than replacing search
    // with `{}` — the same route also carries the list's `?q=` / `?sort=`, and
    // blanking the whole object would silently reset the user's filter.
    void navigate({
      to: '/app/mailboxes',
      search: (prev) => ({ ...prev, connected: undefined, oauth_error: undefined, provider: undefined }),
      replace: true,
    })
    // Runs once: `notice` is a first-render snapshot and never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (dismissed || (!notice.connected && !notice.error)) return null

  if (notice.connected) {
    return (
      <BannerShell tone="ok" onDismiss={() => setDismissed(true)}>
        <CheckCircle2 className="size-4 shrink-0 text-ok" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          {providerLabel} mailbox <span className="font-medium text-foreground">{notice.connected}</span> connected.
        </span>
      </BannerShell>
    )
  }

  return (
    <BannerShell tone="danger" onDismiss={() => setDismissed(true)}>
      <AlertCircle className="size-4 shrink-0 text-danger" aria-hidden="true" />
      <span className="min-w-0 flex-1">{errorMessage(notice.error, providerLabel)}</span>
    </BannerShell>
  )
}

const BANNER_TONE = {
  ok: 'border-ok/30 bg-ok/10',
  warn: 'border-warm/30 bg-warm/10',
  danger: 'border-danger/30 bg-danger/10',
} as const

/**
 * Shared alert chrome (border + tinted background + optional dismiss button) for
 * every full-width notice on the mailboxes page — the OAuth callback outcome,
 * the OAuth "start" error, the post-connect warmup warning, and the standing
 * "pool needs two mailboxes" note — so there is a single alert surface to style
 * and reason about. Callers supply the icon and message as `children`.
 *
 * `onDismiss` is optional: a notice reporting a one-off event is dismissible,
 * but one stating a standing fact about the workspace isn't, since dismissing it
 * wouldn't make it untrue.
 */
export function BannerShell({
  tone,
  onDismiss,
  children,
}: {
  tone: keyof typeof BANNER_TONE
  onDismiss?: () => void
  children: React.ReactNode
}) {
  return (
    <div
      // Only a failure interrupts; `ok`/`warn` are polite so they don't preempt
      // whatever the user is doing.
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cn('flex items-center gap-3 border-b px-5 py-2 text-[13px] text-foreground', BANNER_TONE[tone])}
    >
      {children}
      {onDismiss && (
        <Button variant="ghost" size="icon-sm" className="shrink-0" aria-label="Dismiss" onClick={onDismiss}>
          <X className="size-4" />
        </Button>
      )}
    </div>
  )
}
