import { describe, expect, it } from 'vitest'
import {
  initialRealtimeState,
  isSelfEcho,
  parseEnvelope,
  realtimeReducer,
  type RealtimeEnvelope,
  type RealtimeState,
} from '../socket-events'

function envelope(overrides: Partial<RealtimeEnvelope> = {}): RealtimeEnvelope {
  return {
    seq: 1,
    type: 'inbox.message.created',
    subject: { kind: 'thread', id: 'thread-1' },
    at: '2026-08-27T10:00:00Z',
    actor_id: null,
    data: {},
    ...overrides,
  }
}

function fold(actions: Parameters<typeof realtimeReducer>[1][]): RealtimeState {
  return actions.reduce(realtimeReducer, initialRealtimeState)
}

describe('realtimeReducer', () => {
  it('advances lastSeq for in-order frames', () => {
    const state = fold([
      { kind: 'open' },
      { kind: 'frame', envelope: envelope({ seq: 1 }) },
      { kind: 'frame', envelope: envelope({ seq: 2 }) },
      { kind: 'frame', envelope: envelope({ seq: 3 }) },
    ])
    expect(state.lastSeq).toBe(3)
    expect(state.gapDetected).toBe(false)
    expect(state.status).toBe('live')
  })

  it('ignores a duplicate seq without regressing the cursor', () => {
    const state = fold([
      { kind: 'frame', envelope: envelope({ seq: 5 }) },
      { kind: 'frame', envelope: envelope({ seq: 6 }) },
      { kind: 'frame', envelope: envelope({ seq: 6 }) },
    ])
    expect(state.lastSeq).toBe(6)
    expect(state.gapDetected).toBe(false)
  })

  it('ignores an out-of-order frame that arrives after a newer one', () => {
    const before = fold([{ kind: 'frame', envelope: envelope({ seq: 9 }) }])
    const after = realtimeReducer(before, { kind: 'frame', envelope: envelope({ seq: 4 }) })
    expect(after).toBe(before)
  })

  it('latches gapDetected when a seq is skipped', () => {
    const state = fold([
      { kind: 'frame', envelope: envelope({ seq: 1 }) },
      { kind: 'frame', envelope: envelope({ seq: 4 }) },
    ])
    expect(state.lastSeq).toBe(4)
    expect(state.gapDetected).toBe(true)
    // Latched: a subsequent contiguous frame must not clear it.
    expect(realtimeReducer(state, { kind: 'frame', envelope: envelope({ seq: 5 }) }).gapDetected).toBe(true)
  })

  it('does not report a gap on the very first frame of a fresh connection', () => {
    // A workspace whose feed is already at seq 812 is not a gap for a client
    // that has applied nothing yet.
    const state = fold([{ kind: 'frame', envelope: envelope({ seq: 812 }) }])
    expect(state.gapDetected).toBe(false)
    expect(state.lastSeq).toBe(812)
  })

  it('advances past an unknown event type instead of treating it as a gap', () => {
    const state = fold([
      { kind: 'frame', envelope: envelope({ seq: 1 }) },
      { kind: 'frame', envelope: envelope({ seq: 2, type: 'presence.joined' }) },
      { kind: 'frame', envelope: envelope({ seq: 3 }) },
    ])
    expect(state.lastSeq).toBe(3)
    expect(state.gapDetected).toBe(false)
  })

  it('rejects a non-integer seq rather than poisoning the cursor', () => {
    const state = fold([
      { kind: 'frame', envelope: envelope({ seq: 2 }) },
      { kind: 'frame', envelope: envelope({ seq: Number.NaN }) },
      { kind: 'frame', envelope: envelope({ seq: 1.5 }) },
    ])
    expect(state.lastSeq).toBe(2)
  })

  it('clears the gap only on an explicit resume', () => {
    const gapped = fold([
      { kind: 'frame', envelope: envelope({ seq: 1 }) },
      { kind: 'frame', envelope: envelope({ seq: 7 }) },
    ])
    expect(realtimeReducer(gapped, { kind: 'resumed' }).gapDetected).toBe(false)
  })

  it('reports reconnecting rather than connecting once a frame has been seen', () => {
    expect(realtimeReducer(initialRealtimeState, { kind: 'connecting' }).status).toBe('connecting')
    const resumed = fold([
      { kind: 'frame', envelope: envelope({ seq: 3 }) },
      { kind: 'connecting' },
    ])
    expect(resumed.status).toBe('reconnecting')
  })

  it('counts attempts on close and resets them on open', () => {
    const dropped = fold([{ kind: 'closed' }, { kind: 'closed' }])
    expect(dropped).toMatchObject({ status: 'reconnecting', attempt: 2 })
    expect(realtimeReducer(dropped, { kind: 'open' })).toMatchObject({ status: 'live', attempt: 0 })
  })

  it('goes offline with the terminal reason and keeps the cursor for a manual retry', () => {
    const state = fold([
      { kind: 'frame', envelope: envelope({ seq: 11 }) },
      { kind: 'failed', error: 'Your session expired.' },
    ])
    expect(state).toMatchObject({ status: 'offline', error: 'Your session expired.', lastSeq: 11 })
  })

  it('reset returns the initial state so a workspace switch replays nothing', () => {
    const state = fold([{ kind: 'frame', envelope: envelope({ seq: 40 }) }, { kind: 'reset' }])
    expect(state).toEqual(initialRealtimeState)
  })
})

describe('parseEnvelope', () => {
  it('accepts a well-formed frame', () => {
    const raw = JSON.stringify({
      seq: 3,
      type: 'inbox.message.created',
      subject: { kind: 'thread', id: 'thread-9' },
      at: '2026-08-27T10:00:00Z',
      actor_id: 'user-1',
      data: { mailbox_id: 'mbx-1' },
    })
    expect(parseEnvelope(raw)).toEqual({
      seq: 3,
      type: 'inbox.message.created',
      subject: { kind: 'thread', id: 'thread-9' },
      at: '2026-08-27T10:00:00Z',
      actor_id: 'user-1',
      data: { mailbox_id: 'mbx-1' },
    })
  })

  it.each([
    ['not json', 'not json at all'],
    ['a bare array', '[]'],
    ['a missing seq', '{"type":"pulse.updated","subject":{"kind":"workspace","id":"w"}}'],
    ['a zero seq', '{"seq":0,"type":"pulse.updated","subject":{"kind":"workspace","id":"w"}}'],
    ['a missing type', '{"seq":1,"subject":{"kind":"workspace","id":"w"}}'],
    ['a missing subject', '{"seq":1,"type":"pulse.updated"}'],
    ['a malformed subject', '{"seq":1,"type":"pulse.updated","subject":{"kind":"workspace"}}'],
  ])('returns null (never throws) for %s', (_label, raw) => {
    expect(parseEnvelope(raw)).toBeNull()
  })

  it('returns null for binary frames rather than coercing them', () => {
    expect(parseEnvelope(new ArrayBuffer(4))).toBeNull()
  })

  it('tolerates an unknown type — the envelope is versionless by design', () => {
    const raw = '{"seq":2,"type":"presence.joined","subject":{"kind":"user","id":"u1"}}'
    expect(parseEnvelope(raw)?.type).toBe('presence.joined')
  })
})

describe('isSelfEcho', () => {
  it('drops an event this user originated', () => {
    expect(isSelfEcho(envelope({ actor_id: 'user-1' }), 'user-1')).toBe(true)
  })

  it('keeps an event another user originated', () => {
    expect(isSelfEcho(envelope({ actor_id: 'user-2' }), 'user-1')).toBe(false)
  })

  it('keeps a worker-originated event, which has no actor', () => {
    expect(isSelfEcho(envelope({ actor_id: null }), 'user-1')).toBe(false)
    expect(isSelfEcho(envelope({ actor_id: undefined }), 'user-1')).toBe(false)
  })

  it('keeps everything when the viewer identity is unknown', () => {
    expect(isSelfEcho(envelope({ actor_id: 'user-1' }), null)).toBe(false)
  })
})
