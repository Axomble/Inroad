import { Loader2, MailWarning } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useAppSelector } from '@/store/hooks'
import { useAuthMeQuery, useAuthResendVerificationMutation } from './api'

/**
 * Sits above the app content whenever the signed-in user hasn't confirmed
 * their email yet. Verification isn't tracked in the JWT/session (it's
 * checked fresh by DB lookup — see RequireVerified), so this reads it from
 * `/auth/me`, tagged `Session:CURRENT`. The base query invalidates that same
 * tag whenever any gated action (campaign launch, mailbox create) comes back
 * 403 `email_not_verified` — since this banner is mounted for every
 * authenticated route, that refetch is what makes it appear right away
 * instead of waiting for its next natural poll.
 */
export function UnverifiedBanner() {
  const authed = useAppSelector((s) => s.auth.status === 'authed')
  const { data } = useAuthMeQuery(undefined, { skip: !authed })
  const [resend, { isLoading, isSuccess, isError }] = useAuthResendVerificationMutation()

  if (!data || data.email_verified) return null

  return (
    <div
      role="status"
      className="flex items-center gap-3 border-b border-warn/30 bg-warn/10 px-5 py-2 text-[13px] text-foreground"
    >
      <MailWarning className="size-4 shrink-0 text-warm" aria-hidden="true" />
      <span className="min-w-0 flex-1 truncate">
        Please verify your email address — sending campaigns and connecting mailboxes are blocked until you do.
      </span>
      {isSuccess && <span className="shrink-0 text-xs text-ok">Verification email sent</span>}
      <Button
        variant="chip"
        size="xs"
        className="shrink-0"
        disabled={isLoading}
        onClick={() => {
          void resend()
        }}
      >
        {isLoading && <Loader2 className="animate-spin" />}
        {isError ? 'Try again' : isSuccess ? 'Resend' : 'Resend email'}
      </Button>
    </div>
  )
}
