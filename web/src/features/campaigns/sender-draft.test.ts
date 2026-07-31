import { describe, expect, test } from 'vitest'
import { ROTATION_MODES, fromDraft, rotationModeDescription, senderErrorMessage, toDraft } from './sender-draft'
import type { DraftSender } from './sender-draft'

const POOL = {
  rotation_mode: 'round_robin' as const,
  senders: [
    {
      mailbox_id: 'mb-2',
      email: 'b@example.com',
      provider: 'gmail',
      status: 'active',
      weight: 7,
      enabled: false,
      assigned_count: 12,
      last_assigned_at: '2026-07-31T10:00:00Z',
    },
  ],
}

const MAILBOXES = [
  { id: 'mb-1', email: 'a@example.com', provider: 'smtp', status: 'active' },
  { id: 'mb-2', email: 'b@example.com', provider: 'gmail', status: 'active' },
  { id: 'mb-3', email: 'c@example.com', provider: 'smtp', status: 'paused' },
]

function row(overrides: Partial<DraftSender> = {}): DraftSender {
  return {
    mailbox_id: 'mb-1',
    email: 'a@example.com',
    included: true,
    weight: '1',
    enabled: true,
    assignedCount: 0,
    lastAssignedAt: null,
    ...overrides,
  }
}

describe('toDraft', () => {
  test('offers active mailboxes and marks the pool members', () => {
    const rows = toDraft(POOL, MAILBOXES)

    // mb-3 is paused and not in the pool, so it isn't offered.
    expect(rows.map((r) => r.mailbox_id)).toEqual(['mb-1', 'mb-2'])
    expect(rows[0]).toMatchObject({ included: false, weight: '1', enabled: true })
    expect(rows[1]).toMatchObject({ included: true, weight: '7', enabled: false, assignedCount: 12 })
  })

  test('keeps an inactive mailbox that is already in the pool', () => {
    const pool = {
      rotation_mode: 'weighted' as const,
      senders: [{ ...POOL.senders[0]!, mailbox_id: 'mb-3', email: 'c@example.com', status: 'paused' }],
    }
    const rows = toDraft(pool, MAILBOXES)

    // Dropping it would silently delete it from the pool: the PUT is a full replace.
    expect(rows.find((r) => r.mailbox_id === 'mb-3')).toMatchObject({ included: true, status: 'paused' })
  })

  test('keeps a pool member the mailbox list never returned', () => {
    const rows = toDraft(POOL, [MAILBOXES[0]!])

    expect(rows.map((r) => r.mailbox_id)).toEqual(['mb-1', 'mb-2'])
    expect(rows[1]).toMatchObject({ email: 'b@example.com', included: true, weight: '7' })
  })

  test('is empty when the workspace has no offerable mailboxes', () => {
    expect(toDraft(undefined, [])).toEqual([])
    expect(toDraft({ rotation_mode: 'weighted', senders: [] }, [MAILBOXES[2]!])).toEqual([])
  })
})

describe('fromDraft', () => {
  test('sends only the included rows, with parsed weights', () => {
    const result = fromDraft('weighted', [
      row({ mailbox_id: 'mb-1', weight: '3' }),
      row({ mailbox_id: 'mb-2', email: 'b@example.com', included: false, weight: '9' }),
      row({ mailbox_id: 'mb-3', email: 'c@example.com', weight: '100', enabled: false }),
    ])

    expect(result).toEqual({
      pool: {
        rotation_mode: 'weighted',
        senders: [
          { mailbox_id: 'mb-1', weight: 3, enabled: true },
          { mailbox_id: 'mb-3', weight: 100, enabled: false },
        ],
      },
    })
  })

  test('refuses an empty pool', () => {
    expect(fromDraft('weighted', [row({ included: false })])).toEqual({
      problem: expect.stringContaining('at least one mailbox'),
    })
  })

  test.each(['0', '101', '', ' ', '2.5', '-1', 'ten'])('refuses weight %j', (weight) => {
    expect(fromDraft('weighted', [row({ weight })])).toEqual({
      problem: 'a@example.com: weight must be a whole number from 1 to 100.',
    })
  })

  test('ignores an unsavable weight on an excluded row', () => {
    const result = fromDraft('weighted', [
      row({ mailbox_id: 'mb-1', included: false, weight: '' }),
      row({ mailbox_id: 'mb-2', email: 'b@example.com', weight: '5' }),
    ])

    expect(result).toEqual({
      pool: { rotation_mode: 'weighted', senders: [{ mailbox_id: 'mb-2', weight: 5, enabled: true }] },
    })
  })
})

describe('rotationModeDescription', () => {
  test('explains every mode in the contract', () => {
    for (const mode of ROTATION_MODES) {
      expect(rotationModeDescription(mode.value).length).toBeGreaterThan(20)
    }
  })
})

describe('senderErrorMessage', () => {
  test("prefers the server's reason on a 422", () => {
    const message = senderErrorMessage({ status: 422, data: { error: 'mailbox is not active' } })
    expect(message).toContain('mailbox is not active')
  })

  test('falls back to the contract rules when the 422 carries no reason', () => {
    expect(senderErrorMessage({ status: 422, data: {} })).toContain('weight from 1 to 100')
  })

  test('maps a missing campaign and an unknown failure', () => {
    expect(senderErrorMessage({ status: 404, data: {} })).toBe('This campaign no longer exists.')
    expect(senderErrorMessage({ status: 'FETCH_ERROR', error: 'boom' })).toContain("Couldn't save the senders")
  })
})
