// The words the failed-task queue is made of.
//
// Kept out of JSX for the reason the warmup copy modules give: the wording IS the
// feature. Every row here is work the system gave up on — a send that never went
// out, a reply that never left — and the two mistakes available are opposite. Read
// as noise, an operator ignores lost mail. Read as a retry button, they re-deliver
// mail they did not mean to send.
//
// Four distinctions this module exists to hold:
//
//   REPLAY DELIVERS MAIL. That is why the API asks for campaigns:send rather than
//   campaigns:write, and it is the one thing a button labelled "Retry" would hide.
//   The copy says what the action does before it is taken, not after.
//
//   A CONFLICT IS NOT A FAILURE. 409 means the row was already replayed or
//   discarded — a second tab, a colleague, or a double-click. The replay is
//   exactly-once by a status-guarded claim in Postgres, so a 409 means the system
//   worked. Reported as "already handled", never as an error to try again.
//
//   422 IS PERMANENT. The contract is explicit: the captured payload cannot be
//   replayed and this row will never succeed. Offering another attempt would be
//   inviting the operator to retry something the server has told us is final.
//
//   task_type IS AN OPEN VOCABULARY. The contract says so in as many words — new
//   handlers add new types — so a type this build does not recognise is shown as it
//   arrived. Folding it into "other" would rename work nobody can then find.
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import type { TaskDeadLetter } from '@/store/api'

/** The lifecycle the contract constrains a row to. */
export type DeadLetterStatus = TaskDeadLetter['status']

/** The filter the list screen offers, plus the "everything" case the API's omitted param means. */
export type StatusFilter = DeadLetterStatus | 'all'

interface StatusCopy {
  label: string
  /** What this state means, including whether anything can still be done about it. */
  detail: string
  /** False for the two terminal states: nothing may act on the row again. */
  actionable: boolean
}

/**
 * Keyed by the contract's own union, so a status added to `api/openapi.yaml` fails
 * to compile until it has copy — the same guard the warmup dimension and
 * destination vocabularies use.
 */
export const STATUS_COPY: Record<DeadLetterStatus, StatusCopy> = {
  pending: {
    label: 'Untriaged',
    detail: 'Given up on and not yet dealt with. This is the only state anything can still be done from.',
    actionable: true,
  },
  replayed: {
    label: 'Replayed',
    detail: 'Re-enqueued from the captured payload. Terminal: the work is back in the queue and this row will not change again.',
    actionable: false,
  },
  discarded: {
    label: 'Discarded',
    detail: 'Filed without re-running. Terminal, and it does not mean the work succeeded — it means someone decided not to repeat it.',
    actionable: false,
  },
}

export function statusCopy(status: string): StatusCopy {
  return Object.hasOwn(STATUS_COPY, status)
    ? STATUS_COPY[status as DeadLetterStatus]
    : {
        label: status,
        detail: `This build has no reading for the state "${status}". It is named as it arrived rather than folded into one it knows.`,
        // Unknown means unknown: offering actions on a state we cannot interpret is
        // the one response that could make things worse.
        actionable: false,
      }
}

/* ---------------------------------------------------------------- the screen */

export const PAGE_INTRO =
  'Background work the system tried, retried, and finally gave up on — a send whose provider kept rejecting it, a reply whose mailbox stayed unreachable. Each row is a task that did not run, kept with the payload it was carrying so it can be re-run.'

/**
 * The empty list, which is good news and is written as such.
 *
 * An "It's quiet here" shrug would be the wrong register in both directions: it
 * treats a real all-clear as an absence, and on a filtered view it would suggest
 * nothing has ever failed when the operator is looking at one slice of the queue.
 */
export const EMPTY_ALL =
  'No background task has been given up on. Nothing has been silently dropped — this list fills only when a task exhausts every retry.'

// Does not repeat the heading it sits under, which already names the empty state.
// Its job is the part the heading cannot say: that this is one slice of the queue.
export function emptyFiltered(filter: DeadLetterStatus): string {
  return `Other states may still have rows — clear the filter to see the whole queue. This view is only ${statusCopy(filter).label.toLowerCase()} tasks.`
}

/**
 * This page emptied under the operator while they worked it — every row triaged, or
 * the rows moved out of the filter. Distinct from "the queue is clear": earlier
 * pages may be full, and saying the filter is empty here is the same lie the read
 * error must not tell.
 */
export const EMPTY_PAGE_WITH_HISTORY =
  'The rows that were here have been dealt with, or have moved out of this filter. Earlier pages may still have rows — go back to check.'

/**
 * Shown after a cursor is refused and the list has already returned to page one, so
 * it reports a recovery rather than asking for one.
 */
export const STALE_CURSOR_NOTICE =
  'That page link expired, so the list went back to the first page. Nothing failed and nothing was lost — page links stop working when the server is updated.'

/* ---------------------------------------------------------------- the actions */

/**
 * What replay does, said before it is done. Deliberately not "Retry": a retry is
 * something a system does to itself, and this one puts mail on the wire.
 */
export const REPLAY_CONFIRM =
  'Re-enqueue this task exactly as it was captured. If it is a send, this delivers mail. The payload cannot be edited — replay re-runs what was recorded, and nothing else.'

export const DISCARD_CONFIRM =
  'File this without re-running it. The work does not happen and the row stops being untriaged. This cannot be undone, and it is not a way to make the task succeed.'

/* ---------------------------------------------------------------- the errors */

/**
 * A failed READ of the queue.
 *
 * The specific danger here: an empty list is the ordinary, reassuring state, so a
 * failed request rendered as "nothing has failed" tells an operator their mail is
 * fine when nothing is known. Every branch reads as a request that failed.
 */
export function deadLetterErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so whether any task has failed is unknown right now. Check your connection and try again.'
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return "You don't have access to this workspace's failed tasks."
    case 400:
      // The cursor this PR introduced. Only reachable when a held token outlives the
      // encoding that minted it (a cursorVersion bump, a rolling deploy), so it is
      // reported as a recovery that already happened rather than as a failure to act on.
      return STALE_CURSOR_NOTICE
    case 422:
      return 'That status filter is not one the server recognises. Clear it to see the whole queue.'
    default:
      return serverDetail(error) ?? fallback
  }
}

/**
 * A failed ACTION on one row, which needs its own vocabulary because two of its
 * statuses are not failures of the operator's making and one is permanent.
 *
 * `action` names what was attempted so a 403 can say which permission is missing
 * without this function learning the permission model.
 */
export function deadLetterActionMessage(error: unknown, action: 'replay' | 'discard'): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return `Could not reach the server, so the ${action} did not happen. Nothing changed — try again.`
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return action === 'replay'
        ? 'Replaying re-sends mail, so it needs send permission on campaigns — which this account does not have.'
        : 'Discarding a failed task needs send permission on campaigns, which this account does not have.'
    case 404:
      return 'This task is no longer in the queue — someone may have already dealt with it. Refresh the list.'
    case 409:
      // The exactly-once claim did its job. Saying "failed" here would report the
      // guarantee working as though it had broken.
      return 'Already handled — this task was replayed or discarded elsewhere, so nothing was run twice. Refresh to see its current state.'
    case 422:
      return 'This task cannot be replayed: the captured payload is not one this workspace can re-run. That is permanent for this row — discard it rather than trying again.'
    default:
      return serverDetail(error) ?? `The ${action} failed. Refresh the list and try again.`
  }
}

/* ----------------------------------------------------------------- the row */

/**
 * The last error, or the honest absence of one.
 *
 * The contract allows an empty `last_error`, and blank space in that column would
 * read as "it failed for no reason" — a task that recorded nothing is a different
 * fact from one that failed quietly.
 */
export function lastErrorText(lastError: string): string {
  const trimmed = lastError.trim()
  return trimmed === '' ? 'No error text was recorded for the final attempt.' : trimmed
}

/**
 * Attempts, spelled out. `attempt_count` includes the attempt that failed last, so
 * "3 attempts" is three tries and not three retries after a first — a distinction
 * an operator reading it as retries would get wrong by one.
 */
export function attemptsText(attemptCount: number): string {
  return `${attemptCount.toLocaleString()} attempt${attemptCount === 1 ? '' : 's'} before giving up`
}

/**
 * The payload as text for the disclosure.
 *
 * No try/catch: `payload` is always the product of JSON.parse on the wire response,
 * so it can be neither cyclic nor a BigInt — the only two things that make
 * JSON.stringify throw. Guarding it meant a branch production cannot reach and copy
 * that can never render, reachable only by a test that manufactured a cycle by hand.
 *
 * Pretty-printed rather than one line: it is read to find an id.
 *
 * NO CREDENTIAL IS IN HERE, AND THAT IS NOT THE SAME AS "NOTHING TO REDACT" — an
 * earlier version of this comment made exactly that leap and it was wrong. Secrets
 * are resolved by the worker at execution time and never travel in a task, so the
 * credential half always held. CONTENT was the part it missed: `inbox:reply_send`
 * carried `body_text`, the operator's free-text reply, and this list is served
 * under campaigns:read while inbox:read is withheld from OAuth grants precisely
 * because reply bodies are correspondence.
 *
 * That hole is closed on the server (security.md invariant 64): a task payload is
 * a pointer now, the reply body lives in its row, and existing rows were redacted.
 * What can still appear is a test-send recipient address — one operator-typed
 * address, the single accepted exception, and contact-class data that
 * contacts:read already grants. Still collapsed by default: a payload is read to
 * find an id, and twenty expanded ones are unreadable.
 */
export function payloadText(payload: unknown): string {
  return JSON.stringify(payload, null, 2)
}
