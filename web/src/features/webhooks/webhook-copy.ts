// The words the outbound-webhooks screen is made of.
//
// Kept out of JSX for the same reason `dead-letter-copy.ts` gives: an operator
// reads this screen to decide whether a receiver is safe to trust with mail
// events, and the one thing every branch here protects against is understating
// that. Two things this module exists to hold:
//
//   THE SECRET IS SHOWN EXACTLY ONCE. `api/openapi.yaml` is explicit —
//   `createWebhookEndpoint` and `rotateWebhookEndpointSecret` return it, and
//   nothing else ever will. Copy that hints it might be retrievable later would
//   be advising an operator to skip the one step (copy it now) that matters.
//
//   THE EVENT VOCABULARY IS CLOSED, BUT THE WIRE IS NOT. `event_types` on a
//   read endpoint is `string[]`, not the literal union the create/patch request
//   bodies use — a server ahead of this build can already be sending a type
//   this build has no copy for. Every reader here falls back to the raw string
//   rather than hiding it, the same guard `dead-letter-copy.ts`'s `statusCopy`
//   uses for an unrecognised status.
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import type { WebhookDelivery, WebhookEndpointInput } from '@/store/api'

/* ------------------------------------------------------------ event types */

/** The subscribable event vocabulary, derived from the create request's own type
 * rather than hand-copied — see the module comment on `WebhookEndpointInput`. */
export type WebhookEventType = NonNullable<WebhookEndpointInput['event_types']>[number]

interface EventTypeCopy {
  label: string
  description: string
}

/**
 * Keyed by the contract's own union, so an event type added to
 * `api/openapi.yaml` fails to compile here until it has copy — the same guard
 * `dead-letter-copy.ts`'s `STATUS_COPY` uses.
 */
export const EVENT_TYPE_COPY: Record<WebhookEventType, EventTypeCopy> = {
  'reply.received': {
    label: 'Reply received',
    description: 'A contact replied to a campaign send.',
  },
  'email.bounced': {
    label: 'Email bounced',
    description: 'A send to a contact was rejected by their mail server.',
  },
  'contact.unsubscribed': {
    label: 'Contact unsubscribed',
    description: 'A contact opted out of future sends.',
  },
}

/** The catalog offered when choosing what to subscribe to, in the fixed
 * order `EVENT_TYPE_COPY` declares it — not `Object.keys` order, which JS does
 * not guarantee stays source order once a key looks numeric. */
export const EVENT_TYPE_CATALOG: readonly WebhookEventType[] = [
  'reply.received',
  'email.bounced',
  'contact.unsubscribed',
]

/** A single event type as read off the wire, which may be a type this build
 * does not recognise (see the module comment). Shown as it arrived rather
 * than folded into "other", so it stays findable in the payload the receiver
 * actually gets. */
export function eventTypeLabel(type: string): string {
  return Object.hasOwn(EVENT_TYPE_COPY, type) ? EVENT_TYPE_COPY[type as WebhookEventType].label : type
}

/** The contract's own rule for an empty `event_types`: subscribed to every
 * event. One constant so the row's badge and the summary sentence below say
 * it identically rather than drifting into two phrasings for one fact. */
export const ALL_EVENTS_LABEL = 'All events'

/**
 * An endpoint's subscription, summarised for the row. The contract's own rule
 * — empty means every event — is stated in words rather than left for the
 * operator to infer from a blank space where a list should be.
 */
export function eventTypesSummary(types: readonly string[]): string {
  return types.length === 0 ? ALL_EVENTS_LABEL : types.map(eventTypeLabel).join(', ')
}

/** The labels to render as badges on a row: `[ALL_EVENTS_LABEL]` for an empty
 * subscription, one label per subscribed type otherwise. */
export function eventTypeBadgeLabels(types: readonly string[]): string[] {
  return types.length === 0 ? [ALL_EVENTS_LABEL] : types.map(eventTypeLabel)
}

/* ------------------------------------------------------------- the screen */

export const PAGE_INTRO =
  "Outbound webhooks push a signed POST to a URL you register whenever a subscribed event happens — a reply comes in, a send bounces, a contact unsubscribes — so another system can react without polling this workspace's API."

export const EMPTY_TITLE = 'No webhook endpoints yet'
export const EMPTY_DESCRIPTION =
  "Register a receiver URL and Inroad will POST it a signed payload for every event it's subscribed to. Nothing is sent anywhere until an endpoint exists."

/* -------------------------------------------------------------- create */

export const CREATE_TITLE = 'Register a webhook endpoint'
export const CREATE_DESCRIPTION =
  'Inroad mints a signing secret for this endpoint and shows it once, right after you create it. Leave every event unchecked to subscribe to all of them.'

/* -------------------------------------------------------------- delete */

export const DELETE_CONFIRM =
  "Stop sending events to this endpoint and delete its delivery log. This can't be undone, and the signing secret — already unrecoverable — goes with it."

/* -------------------------------------------------------------- rotate */

export const ROTATE_CONFIRM =
  "Mint a new signing secret for this endpoint. The current secret stops verifying immediately, so the receiver will reject deliveries until it's updated with the new one."

/* --------------------------------------------------------- secret reveal */

export const SECRET_REVEAL_TITLE = 'Copy the signing secret now'

export const SECRET_REVEAL_CREATE_DESCRIPTION =
  "This is the only time Inroad shows this endpoint's signing secret — it verifies the Inroad-Signature header on every delivery. Store it in the receiver now; it cannot be fetched again."

export const SECRET_REVEAL_ROTATE_DESCRIPTION =
  'This is the only time the new signing secret is shown. The previous secret already stopped verifying, so update the receiver now — this one cannot be fetched again either.'

export const SECRET_REVEAL_UNRECOVERABLE =
  'Treat it like a password: anyone who has it can forge deliveries to this endpoint. If you lose it, rotating mints a new one — there is no way to display this one a second time.'

/* -------------------------------------------------------------- ping */

export const PING_QUEUED_NOTICE =
  'A test delivery was queued. Its outcome — delivered, or the error the receiver sent back — appears in the delivery log below once the worker attempts it.'

/* ----------------------------------------------------------- deliveries */

export const DELIVERY_LOG_EMPTY =
  "No deliveries yet. They appear here once an event fires — or immediately after a test ping, which is the fastest way to confirm the receiver is reachable."

export const STALE_DELIVERY_CURSOR_NOTICE =
  'That page link expired, so the log went back to the first page. Nothing failed and nothing was lost — page links stop working when the server is updated.'

interface DeliveryStatusCopy {
  label: string
  /** Matches `StatusPill`'s tone vocabulary in `components/shared/status-pill.tsx`. */
  tone: 'running' | 'warming' | 'failing'
}

/** Keyed by the contract's own union — same exhaustiveness guard as
 * `EVENT_TYPE_COPY` above. */
export const DELIVERY_STATUS_COPY: Record<WebhookDelivery['status'], DeliveryStatusCopy> = {
  pending: { label: 'Pending', tone: 'warming' },
  delivered: { label: 'Delivered', tone: 'running' },
  failed: { label: 'Failed', tone: 'failing' },
}

/**
 * The last-attempt HTTP status, or the honest absence of one — `null` before
 * a first attempt, or when the failure never reached the HTTP layer (DNS,
 * TLS, a timeout). Both are real facts an operator debugging a receiver needs
 * to tell apart from "the receiver answered and it was an error status".
 */
export function responseStatusText(status: number | null): string {
  return status === null ? 'no response' : `HTTP ${status}`
}

/** POST attempts made so far, spelled out with correct pluralisation. */
export function deliveryAttemptsText(attempts: number): string {
  return `${attempts.toLocaleString()} attempt${attempts === 1 ? '' : 's'}`
}

/* -------------------------------------------------------------- the errors */

/** A failed READ of the endpoint list. */
export function webhookErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so whether any webhook endpoint is registered is unknown right now. Check your connection and try again.'
  }
  if (status === 401) return 'Your session expired. Refresh the page and try again.'
  return serverDetail(error) ?? fallback
}

export type WebhookAction = 'create' | 'delete' | 'rotate' | 'ping'

const ACTION_VERB: Record<WebhookAction, string> = {
  create: 'registering the endpoint',
  delete: 'deleting the endpoint',
  rotate: 'rotating the secret',
  ping: 'sending the test ping',
}

/**
 * A failed ACTION on one endpoint. `action` names what was attempted so the
 * unreachable-server and generic-failure branches read as English rather than
 * a status code, without this function needing to know the permission model.
 */
export function webhookActionMessage(error: unknown, action: WebhookAction): string {
  const status = httpStatus(error)
  const verb = ACTION_VERB[action]
  if (status === undefined) {
    return `Could not reach the server, so ${verb} did not happen. Nothing changed — try again.`
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 404:
      // create never 404s (there is no id yet); the other three act on an id
      // that may already be gone from under the operator.
      return 'This endpoint is no longer here — someone may have already deleted it. Refresh the list.'
    case 422:
      // Only create and (unreached here) update send a body the server
      // validates; the SSRF guard and the event-type enum are the two ways a
      // 422 happens.
      return "That URL was rejected — it's either malformed or resolves to an address the SSRF guard blocks (loopback, private, link-local, unspecified or multicast). An unrecognised event type is rejected the same way."
    default:
      // Never the bare "Something went wrong" — the request that failed is
      // named, so an operator staring at a stack of endpoints knows which
      // action to retry.
      return serverDetail(error) ?? `The request failed while ${verb}. Please try again.`
  }
}

/** A failed READ of one endpoint's delivery log. */
export function deliveryErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so the delivery log could not load. Check your connection and try again.'
  }
  if (status === 401) return 'Your session expired. Refresh the page and try again.'
  if (status === 400) return STALE_DELIVERY_CURSOR_NOTICE
  return serverDetail(error) ?? fallback
}
