// Typed-error message mapping for `draftInboxReply`. A sibling of
// reply-error.ts rather than another branch inside it: drafting and sending
// share no status codes worth collapsing (409 means "nothing to reply to"
// here, "suppressed recipient" there), and the draft failures the user can
// actually act on — no model configured, rate limited — have no send
// equivalent at all.
import { httpStatus, retryAfterSeconds, serverDetail } from '@/lib/rtk-error'

/**
 * The workspace has no usable AI model, so drafting can never succeed until
 * someone configures one. 422 on this route means exactly that: it reads as
 * non-retryable (unlike a 5xx, which invites a pointless retry of something no
 * retry can fix), and it keeps the case branchable on status alone — 409 is
 * already spoken for by "no inbound message to reply to", and two 409s would
 * force this function to parse the error body to tell them apart.
 */
const NO_MODEL_CONFIGURED_STATUS = 422

/**
 * A draft failure in the two shapes the composer renders differently: a
 * `no-model` failure carries a link to AI settings, everything else is a
 * self-contained sentence.
 */
export type DraftReplyError = { kind: 'no-model' | 'message'; text: string }

/**
 * Maps an RTK Query error from `draftInboxReply` to what the composer should
 * say. Every branch tells the user whether to retry, wait, or go fix
 * something — a generic "try again" on the no-model case would have them
 * clicking forever.
 */
export function draftReplyError(error: unknown): DraftReplyError {
  const status = httpStatus(error)
  if (status === undefined) {
    return { kind: 'message', text: 'Could not reach the server. Check your connection and try again.' }
  }
  if (status === NO_MODEL_CONFIGURED_STATUS) {
    return { kind: 'no-model', text: 'No AI model is configured for this workspace, so nothing can be drafted.' }
  }
  switch (status) {
    case 401:
      return { kind: 'message', text: 'Your session expired. Refresh the page and try again.' }
    case 404:
      return { kind: 'message', text: 'This thread no longer exists.' }
    case 409:
      return {
        kind: 'message',
        text: 'There is no inbound message to draft a reply to yet.',
      }
    // The route is rate limited per IP and per workspace and answers with
    // Retry-After, which reaches us because the shared base query folds the
    // header onto the error payload (store/empty-api.ts). Name the delay when
    // we have it: "wait a moment" invites an immediate, wasted retry.
    case 429: {
      const seconds = retryAfterSeconds(error)
      return {
        kind: 'message',
        text:
          seconds === null
            ? 'Drafting is rate limited — wait a moment and try again. Nothing was lost.'
            : `Drafting is rate limited — wait ${seconds} second${seconds === 1 ? '' : 's'} and try again. Nothing was lost.`,
      }
    }
    // 502 (the provider call failed) and 504 (it never answered) both resolve
    // the same way for the user — retry or write it yourself — but the copy
    // reflects what actually happened rather than guessing.
    case 502:
      return {
        kind: 'message',
        text: 'The AI provider call failed. Try again in a moment, or write the reply yourself.',
      }
    case 504:
      return {
        kind: 'message',
        text: 'The AI provider did not respond in time — trying again may work, or write the reply yourself.',
      }
    default:
      break
  }
  if (status >= 500) {
    return {
      kind: 'message',
      text: 'Something went wrong while drafting. Try again in a moment, or write the reply yourself.',
    }
  }
  return { kind: 'message', text: serverDetail(error) ?? "Couldn't draft a reply. Please try again." }
}
