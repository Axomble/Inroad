import { describe, expect, it, vi } from 'vitest'
import { api } from '@/store/api'
import type { AppDispatch, RootState } from '@/store'
import { applyEnvelopeToCache } from '../cache-patch'
import { isSelfEcho, type RealtimeEnvelope } from '../socket-events'

function envelope(overrides: Partial<RealtimeEnvelope> = {}): RealtimeEnvelope {
  return {
    seq: 1,
    type: 'inbox.message.created',
    subject: { kind: 'thread', id: 'thread-1' },
    at: '2026-08-27T12:00:00Z',
    actor_id: null,
    data: {},
    ...overrides,
  }
}

/**
 * A recorder standing in for the store. The assertion that matters is *which
 * cached args* got patched, so the cached-arg selector is stubbed with two live
 * args — a hard-coded `undefined` arg would patch neither.
 */
function recorder(cachedArgs: Record<string, unknown[]>) {
  const patched: Array<{ endpoint: string; arg: unknown }> = []
  const refetched: Array<{ endpoint: string; arg: unknown }> = []

  const selectSpy = vi
    .spyOn(api.util, 'selectCachedArgsForQuery')
    .mockImplementation((_state, endpoint) => (cachedArgs[endpoint as string] ?? []) as never)

  const updateSpy = vi
    .spyOn(api.util, 'updateQueryData')
    .mockImplementation(
      (endpoint, arg) => ({ type: 'patch', endpoint, arg }) as never,
    )

  const initiateSpy = vi
    .spyOn(api.endpoints.getPulse, 'initiate')
    .mockImplementation((arg) => ({ type: 'refetch', arg }) as never)

  const dispatch = ((action: { type?: string; endpoint?: string; arg?: unknown }) => {
    if (action.type === 'patch') patched.push({ endpoint: String(action.endpoint), arg: action.arg })
    if (action.type === 'refetch') refetched.push({ endpoint: 'getPulse', arg: action.arg })
    return action
  }) as unknown as AppDispatch

  return {
    dispatch,
    patched,
    refetched,
    state: {} as RootState,
    restore() {
      selectSpy.mockRestore()
      updateSpy.mockRestore()
      initiateSpy.mockRestore()
    },
  }
}

const twoLiveThreadArgs = {
  listInboxThreads: [{ mailboxId: 'mbx-1' }, { replyClass: 'positive' }],
  getPulse: [undefined],
}

describe('applyEnvelopeToCache', () => {
  it('patches EVERY cached thread-list arg, not a hard-coded undefined', () => {
    const r = recorder(twoLiveThreadArgs)
    expect(applyEnvelopeToCache(envelope(), r.dispatch, r.state)).toBe(true)
    expect(r.patched).toEqual([
      { endpoint: 'listInboxThreads', arg: { mailboxId: 'mbx-1' } },
      { endpoint: 'listInboxThreads', arg: { replyClass: 'positive' } },
    ])
    r.restore()
  })

  it('patches nothing when no thread list is subscribed', () => {
    const r = recorder({ listInboxThreads: [], getPulse: [] })
    expect(applyEnvelopeToCache(envelope(), r.dispatch, r.state)).toBe(true)
    expect(r.patched).toEqual([])
    r.restore()
  })

  it('refetches the pulse aggregate rather than invalidating a shared tag', () => {
    const r = recorder(twoLiveThreadArgs)
    applyEnvelopeToCache(envelope({ type: 'pulse.updated', subject: { kind: 'workspace', id: 'w1' } }), r.dispatch, r.state)
    expect(r.refetched).toEqual([{ endpoint: 'getPulse', arg: undefined }])
    expect(r.patched).toEqual([])
    r.restore()
  })

  it('ignores a message event whose subject is not a thread', () => {
    const r = recorder(twoLiveThreadArgs)
    applyEnvelopeToCache(envelope({ subject: { kind: 'mailbox', id: 'mbx-1' } }), r.dispatch, r.state)
    expect(r.patched).toEqual([])
    r.restore()
  })

  it('ignores a classification event with no reply_class in data', () => {
    const r = recorder(twoLiveThreadArgs)
    applyEnvelopeToCache(envelope({ type: 'inbox.reply.classified', data: {} }), r.dispatch, r.state)
    expect(r.patched).toEqual([])
    r.restore()
  })

  it('patches every cached arg for a classification that carries a class', () => {
    const r = recorder(twoLiveThreadArgs)
    applyEnvelopeToCache(
      envelope({ type: 'inbox.reply.classified', data: { reply_class: 'positive' } }),
      r.dispatch,
      r.state,
    )
    expect(r.patched).toHaveLength(2)
    r.restore()
  })

  it('returns false for an event type this client does not handle, and touches nothing', () => {
    const r = recorder(twoLiveThreadArgs)
    expect(applyEnvelopeToCache(envelope({ type: 'presence.joined' }), r.dispatch, r.state)).toBe(false)
    expect(r.patched).toEqual([])
    expect(r.refetched).toEqual([])
    r.restore()
  })

  it('does not touch the cache for an event this tab originated', () => {
    // The guard lives in use-realtime.ts; this asserts the pair works together —
    // without it the optimistic patch in crm/api.ts snaps back and forth.
    const r = recorder(twoLiveThreadArgs)
    const own = envelope({ actor_id: 'user-1' })
    if (!isSelfEcho(own, 'user-1')) applyEnvelopeToCache(own, r.dispatch, r.state)
    expect(r.patched).toEqual([])

    const theirs = envelope({ actor_id: 'user-2' })
    if (!isSelfEcho(theirs, 'user-1')) applyEnvelopeToCache(theirs, r.dispatch, r.state)
    expect(r.patched).toHaveLength(2)
    r.restore()
  })
})
