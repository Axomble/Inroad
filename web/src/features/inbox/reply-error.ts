// Typed-error message mapping for `sendInboxReply`, its own (component-free)
// module so the composer's inline alert stays a pure function of the error —
// mirrors features/campaigns/step-error.ts.
import { httpStatus } from '@/lib/rtk-error'

/**
 * Maps an RTK Query error from `sendInboxReply` to a human message. Mirrors
 * the status codes `api/openapi.yaml`'s `sendInboxReply` operation documents:
 * 409 covers both "recipient suppressed" and "no inbound message to reply
 * to" (the API doesn't distinguish them in the response body).
 *
 * 422 now covers TWO causes: an empty or oversized body, and the workspace
 * being at its outstanding-sends cap. They are not separable — same status,
 * and the only difference is the server's prose, which is not a contract and
 * would break the moment someone rewords it. So the copy names both rather
 * than guessing, and leads with the cap: the composer already blocks empty and
 * oversized bodies before submit, so a 422 that reaches here is far more likely
 * to be the one the client cannot pre-check.
 *
 * Distinguishing them properly needs a machine-readable code in the error
 * envelope, which is a server change and deliberately not smuggled in here.
 */
export function replyErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 409) return 'This recipient is unsubscribed or suppressed — the reply was not sent.'
  if (status === 422) {
    return 'The reply was not queued. Either too many replies are already waiting to send — check your outbox — or the reply is empty or longer than 100,000 characters.'
  }
  if (status === 404) return 'This thread no longer exists.'
  return "Couldn't send the reply. Please try again."
}
