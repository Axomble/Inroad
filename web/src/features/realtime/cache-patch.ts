import { api } from '@/store/api'
import type { AppDispatch, RootState } from '@/store'
import type { CrmDeal, InboxThreadSummary } from '@/store/api'
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
 * A campaign left draft. Launch is the only transition that emits this event,
 * and it lands on exactly one status — `campaign/status.go` StatusRunning —
 * so "running" is the server's word, not a client guess.
 */
function patchCampaignLaunched(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): void {
  if (envelope.subject.kind !== 'campaign') return
  const campaignId = envelope.subject.id
  for (const arg of api.util.selectCachedArgsForQuery(state, 'listCampaigns')) {
    dispatch(
      api.util.updateQueryData('listCampaigns', arg, (draft) => {
        const campaign = draft.find((item) => item.id === campaignId)
        if (campaign) campaign.status = 'running'
      }),
    )
  }
  // The detail query is per-id, so only the launched campaign's own cache entry
  // is touched — patching the others would write one campaign's status onto
  // another.
  for (const arg of api.util.selectCachedArgsForQuery(state, 'getCampaign')) {
    if (arg.id !== campaignId) continue
    dispatch(
      api.util.updateQueryData('getCampaign', arg, (draft) => {
        draft.status = 'running'
      }),
    )
  }
}

/**
 * A mailbox's lifecycle moved (connect / pause / resume / delete). The status
 * string is the server's own word from `mailbox/service.go announceChanged`;
 * "deleted" is the one value that is not a stored status — the row is gone, so
 * it is removed rather than restyled (a client cannot infer a deletion from
 * silence).
 */
function patchMailboxChanged(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): void {
  if (envelope.subject.kind !== 'mailbox') return
  const status = envelope.data?.status
  if (typeof status !== 'string' || !status) return
  const mailboxId = envelope.subject.id
  for (const arg of api.util.selectCachedArgsForQuery(state, 'listMailboxes')) {
    dispatch(
      api.util.updateQueryData('listMailboxes', arg, (draft) => {
        const index = draft.findIndex((mailbox) => mailbox.id === mailboxId)
        if (index < 0) return
        if (status === 'deleted') {
          draft.splice(index, 1)
          return
        }
        const mailbox = draft[index]
        if (mailbox) mailbox.status = status
      }),
    )
  }
}

/**
 * Another actor moved a deal — this tab's own drags never reach here
 * (`use-realtime.ts` drops self-echoes before the cache, which is what keeps
 * this from fighting the optimistic patch in `crm/api.ts`).
 *
 * The envelope names the deal and its new stage, nothing about ordering (the
 * mover's before/after ids are their local intent), so the card lands at the
 * end of the target column and the exact position reconciles on the next board
 * fetch. A board that doesn't hold the deal, or doesn't have the stage (another
 * pipeline), is left alone.
 */
function patchDealMoved(
  envelope: RealtimeEnvelope,
  dispatch: AppDispatch,
  state: RootState,
): void {
  if (envelope.subject.kind !== 'deal') return
  const stageId = envelope.data?.stage_id
  if (typeof stageId !== 'string' || !stageId) return
  const dealId = envelope.subject.id
  for (const arg of api.util.selectCachedArgsForQuery(state, 'crmGetBoard')) {
    dispatch(
      api.util.updateQueryData('crmGetBoard', arg, (draft) => {
        const source = draft.stages.find((column) => column.deals.some((deal) => deal.id === dealId))
        const target = draft.stages.find(({ stage }) => stage.id === stageId)
        if (!source || !target || source === target) return
        const index = source.deals.findIndex((deal) => deal.id === dealId)
        const moved: CrmDeal | undefined = source.deals[index]
        if (!moved) return
        source.deals.splice(index, 1)
        source.deal_count -= 1
        source.amount_micros -= moved.amount_micros ?? 0
        moved.stage_id = stageId
        moved.stage_label = target.stage.label
        moved.stage_color = target.stage.color
        moved.stage_is_won = target.stage.is_won
        moved.stage_is_lost = target.stage.is_lost
        target.deals.push(moved)
        target.deal_count += 1
        target.amount_micros += moved.amount_micros ?? 0
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
    case 'campaign.launched':
      patchCampaignLaunched(envelope, dispatch, state)
      // The console's "running campaigns" count is a pulse aggregate.
      refetchPulse(dispatch, state)
      return true
    case 'mailbox.changed':
      patchMailboxChanged(envelope, dispatch, state)
      // Mailbox counts on the nav derive from status, so the pulse moved too.
      refetchPulse(dispatch, state)
      return true
    case 'deal.moved':
      patchDealMoved(envelope, dispatch, state)
      return true
    case 'send.bounced':
      // The payload is at most an enrollment id — nothing cached is keyed by
      // it, so the bounce/deliverability counts in the pulse are the one thing
      // this event can honestly move.
      refetchPulse(dispatch, state)
      return true
    default:
      // An unhandled type is inert by design: the envelope is versionless so a
      // server ahead of this client must not break it.
      return false
  }
}
