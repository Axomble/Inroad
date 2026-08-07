// Typed-error message mapping for `sendInboxReply`, its own (component-free)
// module so the composer's inline alert stays a pure function of the error —
// mirrors features/campaigns/step-error.ts.
import { httpStatus } from '@/lib/rtk-error'

/**
 * Maps an RTK Query error from `sendInboxReply` to a human message. Mirrors
 * the status codes `api/openapi.yaml`'s `sendInboxReply` operation documents:
 * 409 covers both "recipient suppressed" and "no inbound message to reply
 * to" (the API doesn't distinguish them in the response body), 422 is an
 * empty or oversized body the client-side checks should already have caught.
 */
export function replyErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 409) return 'This recipient is unsubscribed or suppressed — the reply was not sent.'
  if (status === 422) return 'The reply must not be empty, and cannot exceed 100,000 characters.'
  if (status === 404) return 'This thread no longer exists.'
  return "Couldn't send the reply. Please try again."
}
