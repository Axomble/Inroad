import { GatedActionButton } from '@/components/shared/gated-action-button'
import type { ButtonProps } from '@/components/ui/button'
import { emailVerificationHint, useEmailVerified } from './use-email-verified'

/**
 * A Button for an action the server gates behind a confirmed email address
 * (`auth.RequireVerified`). Owns the whole gate — the verification state and the
 * explanation — so a call site names only what the action is:
 *
 *     <VerifiedGateButton action="connect a mailbox" variant="primary" size="sm">
 *
 * The feature-side adapter for the reason-agnostic `GatedActionButton`, which
 * stays in `components/shared` knowing nothing about email verification (so it
 * can serve a plan limit or a missing scope next, and `shared` never imports
 * `features/*`). This wrapper is the auth feature's own concern, which is why it
 * — rather than the shared control — is the thing other features import.
 *
 * Fails open: while `/auth/me` is still in flight the button is NOT blocked, so
 * a verified operator never watches their own controls flicker disabled. The
 * server check is the authority regardless, which is why every gated action
 * still maps a 403 `email_not_verified` to its own copy.
 *
 * See `GatedActionButton` for why blocked means `aria-disabled` and not
 * `disabled` — that accessibility contract lives there and applies here.
 */
export function VerifiedGateButton({
  action,
  ...rest
}: ButtonProps & {
  /** Infinitive phrase completing "Verify your email address to …". */
  action: string
}) {
  const { verified } = useEmailVerified()
  return <GatedActionButton blocked={!verified} reason={emailVerificationHint(action)} {...rest} />
}
