import { describe, expect, it, vi } from 'vitest'
import { api } from '@/store/api'
import type { AppDispatch, RootState } from '@/store'
import type {
  Campaign,
  CampaignDetail,
  CrmBoard,
  CrmBoardStage,
  CrmDeal,
  CrmStage,
  InboxThreadPage,
  InboxThreadSummary,
  Mailbox,
} from '@/store/api'
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

function runRecipeFor<T>(
  endpoint: string,
  arg: unknown,
  data: T,
  frame: RealtimeEnvelope,
): T {
  const selectSpy = vi
    .spyOn(api.util, 'selectCachedArgsForQuery')
    .mockImplementation((_state, name) => (name === endpoint ? [arg] : []) as never)
  const updateSpy = vi
    .spyOn(api.util, 'updateQueryData')
    .mockImplementation((_endpoint, _arg, recipe) => {
      ;(recipe as (draft: T) => void)(data)
      return { type: 'noop' } as never
    })
  const dispatch = ((action: unknown) => action) as unknown as AppDispatch
  applyEnvelopeToCache(frame, dispatch, {} as RootState)
  selectSpy.mockRestore()
  updateSpy.mockRestore()
  return data
}

function runRecipe(page: InboxThreadPage, frame: RealtimeEnvelope): InboxThreadPage {
  return runRecipeFor('listInboxThreads', {}, page, frame)
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

function mailbox(id: string, status: string): Mailbox {
  return { id, email: `${id}@example.com`, status }
}

function launched(id: string): RealtimeEnvelope {
  return envelope({
    type: 'campaign.launched',
    subject: { kind: 'campaign', id },
    data: { campaign_id: id, enrolled: 3 },
  })
}

describe('mailbox.changed recipe', () => {
  const changed = (id: string, status: string): RealtimeEnvelope =>
    envelope({ type: 'mailbox.changed', subject: { kind: 'mailbox', id }, data: { mailbox_id: id, status } })

  it('updates only the named mailbox status', () => {
    const list = runRecipeFor(
      'listMailboxes',
      undefined,
      [mailbox('mbx-1', 'active'), mailbox('mbx-2', 'active')],
      changed('mbx-2', 'paused'),
    )
    expect(list.map((m) => m.status)).toEqual(['active', 'paused'])
  })

  it('removes the row on the "deleted" status instead of styling it', () => {
    const list = runRecipeFor(
      'listMailboxes',
      undefined,
      [mailbox('mbx-1', 'active'), mailbox('mbx-2', 'error')],
      changed('mbx-2', 'deleted'),
    )
    expect(list.map((m) => m.id)).toEqual(['mbx-1'])
  })

  it('leaves the list untouched for a mailbox that is not cached', () => {
    const list = runRecipeFor(
      'listMailboxes',
      undefined,
      [mailbox('mbx-1', 'active')],
      changed('mbx-zz', 'paused'),
    )
    expect(list).toEqual([mailbox('mbx-1', 'active')])
  })
})

describe('campaign.launched recipe', () => {
  it('flips the launched campaign to running in the list, leaving the rest alone', () => {
    const list: Campaign[] = [
      { id: 'camp-a', name: 'A', status: 'draft' },
      { id: 'camp-b', name: 'B', status: 'draft' },
    ]
    runRecipeFor('listCampaigns', undefined, list, launched('camp-b'))
    expect(list.map((c) => c.status)).toEqual(['draft', 'running'])
  })

  it('flips the detail entry whose arg matches the launched id', () => {
    const detail: CampaignDetail = { id: 'camp-b', name: 'B', status: 'draft' }
    runRecipeFor('getCampaign', { id: 'camp-b' }, detail, launched('camp-b'))
    expect(detail.status).toBe('running')
  })

  it('never writes into a different campaign detail entry', () => {
    const detail: CampaignDetail = { id: 'camp-a', name: 'A', status: 'draft' }
    runRecipeFor('getCampaign', { id: 'camp-a' }, detail, launched('camp-b'))
    expect(detail.status).toBe('draft')
  })

  it('leaves a list without the campaign untouched', () => {
    const list: Campaign[] = [{ id: 'camp-a', name: 'A', status: 'draft' }]
    runRecipeFor('listCampaigns', undefined, list, launched('camp-zz'))
    expect(list[0]?.status).toBe('draft')
  })
})

function stage(id: string, label: string): CrmStage {
  return {
    id,
    pipeline_id: 'pipe-1',
    key: label.toLowerCase(),
    label,
    color: '#888888',
    position: 0,
    is_won: label === 'Won',
    is_lost: false,
    created_at: '2026-08-27T12:00:00Z',
    updated_at: '2026-08-27T12:00:00Z',
  }
}

function deal(id: string, stageId: string, amountMicros: number): CrmDeal {
  return {
    id,
    pipeline_id: 'pipe-1',
    stage_id: stageId,
    name: `Deal ${id}`,
    amount_micros: amountMicros,
    currency: 'USD',
    position: 1,
    source: 'manual',
    created_by_actor: {},
    pipeline_name: 'Pipeline',
    stage_label: 'Open',
    stage_color: '#888888',
    stage_is_won: false,
    stage_is_lost: false,
    created_at: '2026-08-27T12:00:00Z',
    updated_at: '2026-08-27T12:00:00Z',
  }
}

function column(stageId: string, label: string, deals: CrmDeal[]): CrmBoardStage {
  return {
    stage: stage(stageId, label),
    deals,
    deal_count: deals.length,
    amount_micros: deals.reduce((sum, d) => sum + (d.amount_micros ?? 0), 0),
  }
}

function board(stages: CrmBoardStage[]): CrmBoard {
  return {
    pipeline: {
      id: 'pipe-1',
      name: 'Pipeline',
      is_default: true,
      stages: stages.map((s) => s.stage),
      created_at: '2026-08-27T12:00:00Z',
      updated_at: '2026-08-27T12:00:00Z',
    },
    stages,
  }
}

describe('deal.moved recipe', () => {
  const moved = (dealId: string, stageId: string): RealtimeEnvelope =>
    envelope({ type: 'deal.moved', subject: { kind: 'deal', id: dealId }, data: { deal_id: dealId, stage_id: stageId } })

  it('moves the card between columns and keeps counts and amounts consistent', () => {
    const b = board([
      column('stage-1', 'Open', [deal('deal-1', 'stage-1', 100), deal('deal-2', 'stage-1', 50)]),
      column('stage-2', 'Won', [deal('deal-3', 'stage-2', 25)]),
    ])
    runRecipeFor('crmGetBoard', { pipelineId: 'pipe-1' }, b, moved('deal-1', 'stage-2'))

    expect(b.stages[0]?.deals.map((d) => d.id)).toEqual(['deal-2'])
    expect(b.stages[0]).toMatchObject({ deal_count: 1, amount_micros: 50 })
    // No ordering on the wire, so the card lands at the end of the column.
    expect(b.stages[1]?.deals.map((d) => d.id)).toEqual(['deal-3', 'deal-1'])
    expect(b.stages[1]).toMatchObject({ deal_count: 2, amount_micros: 125 })
    // The card wears its new stage's identity.
    expect(b.stages[1]?.deals[1]).toMatchObject({
      stage_id: 'stage-2',
      stage_label: 'Won',
      stage_is_won: true,
    })
  })

  it('does nothing when the deal is not on this cached board', () => {
    const b = board([column('stage-1', 'Open', [deal('deal-1', 'stage-1', 100)]), column('stage-2', 'Won', [])])
    runRecipeFor('crmGetBoard', {}, b, moved('deal-zz', 'stage-2'))
    expect(b.stages[0]?.deals).toHaveLength(1)
    expect(b.stages[1]?.deals).toHaveLength(0)
  })

  it('does nothing when the target stage belongs to another board', () => {
    const b = board([column('stage-1', 'Open', [deal('deal-1', 'stage-1', 100)])])
    runRecipeFor('crmGetBoard', {}, b, moved('deal-1', 'stage-elsewhere'))
    expect(b.stages[0]?.deals.map((d) => d.id)).toEqual(['deal-1'])
    expect(b.stages[0]).toMatchObject({ deal_count: 1, amount_micros: 100 })
  })

  it('does nothing when the deal is already in the target column', () => {
    const b = board([column('stage-1', 'Open', [deal('deal-1', 'stage-1', 100)])])
    runRecipeFor('crmGetBoard', {}, b, moved('deal-1', 'stage-1'))
    expect(b.stages[0]?.deals.map((d) => d.id)).toEqual(['deal-1'])
    expect(b.stages[0]).toMatchObject({ deal_count: 1, amount_micros: 100 })
  })
})
