import { api } from '@/store/api'
import type { AppDispatch, RootState } from '@/store'
import type { InboxThreadSummary } from '@/store/api'
import type { RealtimeEnvelope } from './socket-events'

/**
 * Where a socket envelope meets the RTK Query cache.
 *
 * Two rules from spec §6, both non-obvious:
 *
 * 1. **Patch, never invalidate.** Invalidation triggers a refetch, which is the
 *    request the socket exists to remove.
 * 2. **Patch every cached arg.** An envelope arrives knowing nothing about which
 *    query args are live (the thread list is per mailbox, per reply class, per
 *    tz offset), so `selectCachedArgsForQuery` is the only correct technique —
 *    a hard-coded `undefined` arg patches a cache entry nobody subscribed to.
 *    `crm/api.ts:151-153` established this for drag-to-move.
 *
 * `pulse.updated` is the deliberate exception and is handled by
 * `refetchPulse`: its payload is an aggregate of a dozen server-side counts and
 * spec §7 forbids putting a full record on the wire, so there is nothing to
 * patch from. A single targeted refetch of one endpoint still beats a 45s poll
 * per tab.
 */

/** Fold a fresh inbound message into every cached thread list. */
function patchInboxMessageCreated(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): void {
  if (envelope.subject.kind !== 'thread') return
  const threadId = envelope.subject.id
  for (const arg of api.util.selectCachedArgsForQuery(state, 'listInboxThreads')) {
    dispatch(
      api.util.updateQueryData('listInboxThreads', arg, (draft) => {
        const index = draft.items.findIndex((thread) => thread.id === threadId)
        const existing = index >= 0 ? draft.items[index] : undefined
        if (!existing) return
        // Minimal fields only (spec §7): the envelope names the thread and when,
        // never the message body or the recipient. Anything richer is fetched
        // through the authorized endpoint when the user opens the thread.
        const updated: InboxThreadSummary = {
          ...existing,
          unread: true,
          last_message_at: envelope.at || existing.last_message_at,
        }
        draft.items.splice(index, 1)
        // Lists are newest-first, and a new message makes this the newest thread.
        draft.items.unshift(updated)
      }),
    )
  }
}

/** A classifier verdict landed. Only the label key is on the wire. */
function patchReplyClassified(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): void {
  if (envelope.subject.kind !== 'thread') return
  const replyClass = envelope.data?.reply_class
  if (typeof replyClass !== 'string' || !replyClass) return
  const threadId = envelope.subject.id
  for (const arg of api.util.selectCachedArgsForQuery(state, 'listInboxThreads')) {
    dispatch(
      api.util.updateQueryData('listInboxThreads', arg, (draft) => {
        const thread = draft.items.find((item) => item.id === threadId)
        if (thread) thread.last_reply_class = replyClass
      }),
    )
  }
}

/**
 * Re-read the pulse aggregate for whichever args are subscribed. `forceRefetch`
 * on the existing arg, not `invalidateTags`: invalidation would also refetch
 * every other query sharing the tag.
 */
function refetchPulse(dispatch: AppDispatch, state: RootState): void {
  for (const arg of api.util.selectCachedArgsForQuery(state, 'getPulse')) {
    void dispatch(
      api.endpoints.getPulse.initiate(arg, { forceRefetch: true, subscribe: false }),
    )
  }
}

/**
 * Apply one envelope. Returns true when the event was recognised and handled —
 * false means "known to the transport, no cache consequence here yet", which is
 * not an error and must not surface to the user.
 */
export function applyEnvelopeToCache(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): boolean {
  switch (envelope.type) {
    case 'inbox.message.created':
      patchInboxMessageCreated(envelope, dispatch, state)
      refetchPulse(dispatch, state)
      return true
    case 'inbox.reply.classified':
      patchReplyClassified(envelope, dispatch, state)
      return true
    case 'pulse.updated':
      refetchPulse(dispatch, state)
      return true
    default:
      // Slice 7 lands campaign / mailbox / bounce / deal. An unhandled type is
      // inert by design: the envelope is versionless so a server ahead of this
      // client must not break it.
      return false
  }
}
