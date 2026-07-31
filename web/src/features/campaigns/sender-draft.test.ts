import { describe, expect, test } from 'vitest'
import {
  ROTATION_MODES,
  capacityLabel,
  fromDraft,
  gatedReason,
  reducedCapReason,
  rotationModeDescription,
  senderErrorMessage,
  toDraft,
} from './sender-draft'
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
    healthState: null,
    sending: null,
    capToday: null,
    sentToday: null,
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

  test('reads the health and capacity fields the server reported', () => {
    const pool = {
      rotation_mode: 'weighted' as const,
      senders: [
        { ...POOL.senders[0]!, health_state: 'throttled' as const, sending: true, cap_today: 25, sent_today: 4 },
      ],
    }
    expect(toDraft(pool, MAILBOXES).find((r) => r.mailbox_id === 'mb-2')).toMatchObject({
      healthState: 'throttled',
      sending: true,
      capToday: 25,
      sentToday: 4,
    })
  })

  // The fields are optional in the contract, so a response from a server that
  // doesn't send them yet must read as "unknown", never as zero or "not sending".
  test('reads absent health and capacity fields as unknown, not as zero', () => {
    const rows = toDraft(POOL, MAILBOXES)

    for (const r of rows) {
      expect(r).toMatchObject({ healthState: null, sending: null, capToday: null, sentToday: null })
    }
  })

  test('reads a null health state as not warming up rather than healthy', () => {
    const pool = {
      rotation_mode: 'weighted' as const,
      senders: [{ ...POOL.senders[0]!, health_state: null, cap_today: 50, sent_today: 0 }],
    }
    expect(toDraft(pool, MAILBOXES).find((r) => r.mailbox_id === 'mb-2')?.healthState).toBeNull()
  })

  test('reads a zero cap as a real zero, not as unknown', () => {
    const pool = {
      rotation_mode: 'weighted' as const,
      senders: [{ ...POOL.senders[0]!, sending: false, cap_today: 0, sent_today: 0 }],
    }
    const member = toDraft(pool, MAILBOXES).find((r) => r.mailbox_id === 'mb-2')
    expect(member).toMatchObject({ capToday: 0, sentToday: 0, sending: false })
  })
})

describe('capacityLabel', () => {
  test("states today's sends against today's ceiling", () => {
    expect(capacityLabel(row({ sentToday: 18, capToday: 35 }))).toBe('18 / 35 sent today')
    expect(capacityLabel(row({ sentToday: 0, capToday: 0 }))).toBe('0 / 0 sent today')
  })

  // Half a fraction would render as "18 / NaN"; saying nothing is honest.
  test('says nothing when either figure is missing', () => {
    expect(capacityLabel(row())).toBeNull()
    expect(capacityLabel(row({ sentToday: 18 }))).toBeNull()
    expect(capacityLabel(row({ capToday: 35 }))).toBeNull()
  })
})

describe('gatedReason', () => {
  test('names warmup when a paused mailbox is the reason', () => {
    expect(gatedReason(row({ sending: false, healthState: 'paused' }))).toBe(
      'Paused by warmup — not sending',
    )
  })

  test('names an inactive mailbox when that is the reason', () => {
    expect(gatedReason(row({ sending: false, status: 'disconnected' }))).toBe(
      'Mailbox is disconnected — not sending',
    )
  })

  test('falls back to rotation for a gated row with no other explanation', () => {
    expect(gatedReason(row({ sending: false, status: 'active' }))).toBe(
      'Held out of rotation — not sending',
    )
  })

  test('says nothing for a sending row, or when the server did not report it', () => {
    expect(gatedReason(row({ sending: true, healthState: 'paused' }))).toBeNull()
    expect(gatedReason(row())).toBeNull()
  })

  // An unsaved tick is not something the sending engine has acted on yet, so the
  // draft's own booleans must never produce this copy.
  test('ignores the draft rotation checkboxes', () => {
    expect(gatedReason(row({ included: false, enabled: false, sending: true }))).toBeNull()
  })
})

describe('reducedCapReason', () => {
  test.each(['watch', 'throttled'] as const)('explains a lowered cap for %s', (healthState) => {
    expect(reducedCapReason(row({ healthState, capToday: 25 }))).toContain('Cap lowered by warmup health')
  })

  test('says nothing for a healthy, unknown, or absent state', () => {
    expect(reducedCapReason(row({ healthState: 'healthy', capToday: 50 }))).toBeNull()
    expect(reducedCapReason(row({ healthState: 'paused', capToday: 0 }))).toBeNull()
    expect(reducedCapReason(row())).toBeNull()
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
