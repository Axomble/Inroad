import { useEffect, useState } from 'react'
import { AlertCircle, X } from 'lucide-react'
import { Button } from '@/components/ui/button'

// Plain, actionable copy per backend reason code. Exhaustive on purpose so an
// unmapped reason falls through to the generic message rather than rendering a
// machine token at the user.
const errorCopy: Record<string, string> = {
  denied: 'You cancelled Google sign-in. Try again, or sign in with your password.',
  bad_state: 'That Google sign-in link expired. Start again.',
  no_email: "Google didn't share an email address for that account. Try another account.",
  // Distinct from `no_email`, and the one reason that needs its own words: Google
  // returned an address it says it has NOT verified, so we refuse to sign up or
  // link on it — that check is what stops someone claiming your address with a
  // Google account. Retrying changes nothing, so the copy points at Google.
  email_unverified:
    "Google hasn't verified that account's email address, so we can't sign you in with it. Verify it with Google, or use another account.",
  disabled: "Google sign-in isn't configured on this server. Use your password instead.",
  invite_invalid:
    'That invitation was issued to a different email address, or has expired. Ask for a new invite, then try again.',
  exchange_failed: "Couldn't finish Google sign-in. Try again.",
  server_error: "Couldn't finish Google sign-in. Try again, or sign in with your password.",
}

const GENERIC_ERROR = "Couldn't finish Google sign-in. Try again, or sign in with your password."

/** Banner copy for a backend reason code, falling back to the generic message. */
function googleErrorMessage(reason: string | undefined): string {
  return (reason && errorCopy[reason]) || GENERIC_ERROR
}

/**
 * Reports a failed Google sign-in once the backend's callback redirects the
 * browser back to the login screen with `?google_error=<reason>`.
 *
 * `reason` is snapshotted on first render, then `onClear` strips the param from
 * the URL — same order as the mailboxes OAuth banner, and for the same reason: if
 * the banner read live search state it would blank itself the moment the
 * replace-navigation lands, and a failed sign-in would silently vanish instead of
 * telling the user what to do next.
 */
export function GoogleCallbackBanner({
  reason,
  onClear,
}: {
  reason: string | undefined
  onClear: () => void
}) {
  const [notice] = useState(reason)
  const [dismissed, setDismissed] = useState(false)

  useEffect(() => {
    if (notice) onClear()
    // Runs once: `notice` is a first-render snapshot and never changes, and
    // `onClear` is a fresh closure on every render of the parent.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (!notice || dismissed) return null

  return (
    <div
      role="alert"
      className="auth-rise mb-5 flex items-start gap-2.5 rounded-md border border-danger/30 bg-danger/10 px-3 py-2.5 text-xs text-danger"
    >
      <AlertCircle className="mt-px size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1 leading-relaxed">{googleErrorMessage(notice)}</span>
      <Button
        variant="ghost"
        size="icon-sm"
        className="-my-1 -mr-1 shrink-0 text-danger hover:bg-danger/15 hover:text-danger"
        aria-label="Dismiss"
        onClick={() => setDismissed(true)}
      >
        <X className="size-3.5" />
      </Button>
    </div>
  )
}
