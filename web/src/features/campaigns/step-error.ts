// Shared typed-error message mapping for sequence-step mutations. Lives in its
// own (component-free) module so both the add/edit form and the delete dialog
// surface the SAME human copy for the same HTTP status, narrowed via the typed
// `httpStatus` helper instead of ad-hoc `'status' in err` checks.
import { errorCode, httpStatus, isEmailNotVerified } from '@/lib/rtk-error'
// Read-only cross-feature reuse of the auth feature's copy helper —
// component-free module, the established exception (see mailboxes-page.tsx
// pulling the warmup query). One sentence for both the disabled control's
// explanation and the 403 this endpoint can still answer.
import { emailVerificationHint } from '@/features/auth/use-email-verified'

/** Completes `emailVerificationHint`'s sentence; one wording for gate + error. */
export const TEST_SEND_GATED_ACTION = 'send a test email'

/**
 * A delete or reorder moves the linear fall-through, and with a condition in
 * the sequence that can close a loop — which the server refuses (422 `cycle`).
 */
const LOOP_COPY =
  'That would make a loop through a condition — someone could be sent the same step twice. Change the order, or one of the condition’s exits.'

/** Maps an RTK Query error from a step mutation to a human message. */
export function stepErrorMessage(error: unknown): string {
  if (errorCode(error) === 'cycle') return LOOP_COPY
  const status = httpStatus(error)
  if (status === 409) return 'Structural changes are only allowed while the campaign is a draft.'
  if (status === 404) return 'That step no longer exists.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't save the step. Please try again."
}

/**
 * Maps an RTK Query error from `reorderSteps`. Shared by the list's drag handle
 * and the canvas's move/connect gestures so one failure reads the same way
 * from either view.
 */
export function reorderErrorMessage(error: unknown): string {
  if (errorCode(error) === 'cycle') return LOOP_COPY
  if (httpStatus(error) === 409) return 'Reorder is only allowed while the campaign is a draft.'
  return "Couldn't reorder steps — try again."
}

/**
 * Maps an RTK Query error from `testSendCampaign` to a human message. Mirrors
 * the status codes `internal/app/campaign/handler.go`'s `testSend` actually
 * returns: 400 is either a malformed step id or a `to` that failed the
 * backend's `validate:"required,email"` tag, 422 is "no eligible sender",
 * 429 is the per-workspace test-send rate limit.
 */
export function testSendErrorMessage(error: unknown): string {
  // Ahead of the status ladder: /test-send sits behind `auth.RequireVerified`,
  // whose 403 has an answer the operator can act on.
  if (isEmailNotVerified(error)) return emailVerificationHint(TEST_SEND_GATED_ACTION)
  const status = httpStatus(error)
  if (status === 400) return 'Enter a valid email address.'
  if (status === 404) return 'This step no longer exists.'
  if (status === 422) return 'No enabled sender with a connected mailbox — add one in Senders first.'
  if (status === 429) return 'Too many test sends. Please wait a moment, then try again.'
  return "Couldn't send the test email. Please try again."
}
