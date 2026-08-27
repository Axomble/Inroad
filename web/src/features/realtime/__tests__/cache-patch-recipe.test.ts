import { describe, expect, it, vi } from 'vitest'
import { api } from '@/store/api'
import type { AppDispatch, RootState } from '@/store'
import type { InboxThreadPage, InboxThreadSummary } from '@/store/api'
import { applyEnvelopeToCache } from '../cache-patch'
import type { RealtimeEnvelope } from '../socket-events'

/**
 * The other cache-patch test asserts *which* args are patched. This one runs the
 * recipe itself against a real draft, so the list mutation (unread flag, newest
 * -first reorder, unknown thread ignored) is behavior, not an untested closure.
 */
function thread(id: string, at: string, overrides: Partial<InboxThreadSummary> = {}): InboxThreadSummary {
  return {
    id,
    mailbox_id: 'mbx-1',
    campaign_id: null,
    contact_id: null,
    contact_email: 'lead@example.com',
    contact_first_name: '',
    contact_last_name: '',
    subject: `Subject ${id}`,
    last_reply_class: 'neutral',
    reply_label: null,
    unread: false,
    last_message_at: at,
    ...overrides,
  }
}

function runRecipe(page: InboxThreadPage, frame: RealtimeEnvelope): InboxThreadPage {
  const selectSpy = vi
    .spyOn(api.util, 'selectCachedArgsForQuery')
    .mockImplementation((_state, endpoint) => (endpoint === 'listInboxThreads' ? [{}] : []) as never)
  const updateSpy = vi
    .spyOn(api.util, 'updateQueryData')
    .mockImplementation((_endpoint, _arg, recipe) => {
      ;(recipe as (draft: InboxThreadPage) => void)(page)
      return { type: 'noop' } as never
    })
  const dispatch = ((action: unknown) => action) as unknown as AppDispatch
  applyEnvelopeToCache(frame, dispatch, {} as RootState)
  selectSpy.mockRestore()
  updateSpy.mockRestore()
  return page
}

function envelope(overrides: Partial<RealtimeEnvelope> = {}): RealtimeEnvelope {
  return {
    seq: 1,
    type: 'inbox.message.created',
    subject: { kind: 'thread', id: 'thread-b' },
    at: '2026-08-27T15:00:00Z',
    actor_id: null,
    data: {},
    ...overrides,
  }
}

describe('inbox.message.created recipe', () => {
  it('marks the thread unread, stamps the time and moves it to the top', () => {
    const page = runRecipe(
      {
        items: [
          thread('thread-a', '2026-08-27T14:00:00Z'),
          thread('thread-b', '2026-08-27T13:00:00Z'),
          thread('thread-c', '2026-08-27T12:00:00Z'),
        ],
      },
      envelope(),
    )

    expect(page.items.map((t) => t.id)).toEqual(['thread-b', 'thread-a', 'thread-c'])
    expect(page.items[0]).toMatchObject({ unread: true, last_message_at: '2026-08-27T15:00:00Z' })
    // Only the named thread changes.
    expect(page.items[1]?.unread).toBe(false)
  })

  it('keeps the existing timestamp when the envelope carries none', () => {
    const page = runRecipe({ items: [thread('thread-b', '2026-08-27T13:00:00Z')] }, envelope({ at: '' }))
    expect(page.items[0]?.last_message_at).toBe('2026-08-27T13:00:00Z')
  })

  it('leaves the list untouched for a thread that is not in this cached page', () => {
    // A brand-new thread is not fabricated from an envelope: spec §7 keeps
    // payloads minimal, so there is no honest summary to insert. The page the
    // user is looking at stays correct; the full row arrives on next fetch.
    const page = runRecipe(
      { items: [thread('thread-a', '2026-08-27T14:00:00Z')] },
      envelope({ subject: { kind: 'thread', id: 'thread-zz' } }),
    )
    expect(page.items).toHaveLength(1)
    expect(page.items[0]).toMatchObject({ id: 'thread-a', unread: false })
  })
})

describe('inbox.reply.classified recipe', () => {
  it('updates only the classified thread', () => {
    const page = runRecipe(
      { items: [thread('thread-a', '2026-08-27T14:00:00Z'), thread('thread-b', '2026-08-27T13:00:00Z')] },
      envelope({ type: 'inbox.reply.classified', data: { reply_class: 'positive' } }),
    )
    expect(page.items.map((t) => t.last_reply_class)).toEqual(['neutral', 'positive'])
    // Classification does not reorder or re-unread the list.
    expect(page.items.map((t) => t.id)).toEqual(['thread-a', 'thread-b'])
    expect(page.items[1]?.unread).toBe(false)
  })
})
