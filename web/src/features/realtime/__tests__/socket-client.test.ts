import { describe, expect, it, vi } from 'vitest'
import {
  backoffDelay,
  missedPongLimit,
  pingIntervalMs,
  RealtimeClient,
  RealtimeHandshakeError,
  requestTicket,
  socketURL,
  type SocketLike,
} from '../socket-client'
import type { RealtimeEnvelope } from '../socket-events'

/** A WebSocket that never touches the network; the test moves it by hand. */
class FakeSocket implements SocketLike {
  onopen: ((event: unknown) => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: ((event: unknown) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  readonly sent: string[] = []
  readonly closes: Array<number | undefined> = []

  readonly url: string

  constructor(url: string) {
    this.url = url
  }

  send(data: string) {
    this.sent.push(data)
  }

  close(code?: number) {
    this.closes.push(code)
  }

  open() {
    this.onopen?.({})
  }

  deliver(data: unknown) {
    this.onmessage?.({ data })
  }

  drop() {
    this.onclose?.({})
  }
}

/**
 * A timer queue the test drains explicitly. `vi.useFakeTimers` would work, but
 * an explicit queue also records the delay each callback was scheduled with —
 * which is the whole assertion for the backoff schedule.
 */
function fakeTimers() {
  const pending = new Map<number, { fn: () => void; ms: number }>()
  let next = 1
  return {
    delays: [] as number[],
    set(fn: () => void, ms: number) {
      const handle = next++
      pending.set(handle, { fn, ms })
      this.delays.push(ms)
      return handle
    },
    clear(handle: number) {
      pending.delete(handle)
    },
    /** Run every queued callback once, in schedule order. */
    async flush() {
      const due = [...pending.entries()]
      for (const [handle, entry] of due) {
        pending.delete(handle)
        entry.fn()
      }
      await Promise.resolve()
      await Promise.resolve()
    },
    get size() {
      return pending.size
    },
  }
}

function harness(
  overrides: {
    fetchTicket?: (token: string) => Promise<{ ticket: string; expires_in: number }>
    lastSeq?: () => number
    random?: () => number
  } = {},
) {
  const sockets: FakeSocket[] = []
  const timers = fakeTimers()
  const envelopes: RealtimeEnvelope[] = []
  const events: string[] = []
  let fatal: string | null = null
  const client = new RealtimeClient({
    token: 'token-1',
    lastSeq: overrides.lastSeq ?? (() => 0),
    onOpen: () => events.push('open'),
    onEnvelope: (envelope) => envelopes.push(envelope),
    onClose: () => events.push('close'),
    onFatal: (message) => {
      fatal = message
      events.push('fatal')
    },
    createSocket: (url) => {
      const socket = new FakeSocket(url)
      sockets.push(socket)
      return socket
    },
    fetchTicket:
      overrides.fetchTicket ?? (() => Promise.resolve({ ticket: 'tkt-1', expires_in: 30 })),
    setTimeout: (fn, ms) => timers.set(fn, ms),
    clearTimeout: (handle) => timers.clear(handle),
    // Midpoint jitter keeps the asserted schedule deterministic.
    random: overrides.random ?? (() => 0.5),
  })
  return {
    client,
    sockets,
    timers,
    envelopes,
    events,
    get fatal() {
      return fatal
    },
    /** Let the ticket promise settle so the socket exists. */
    async settle() {
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    },
  }
}

function envelopeFrame(seq: number): string {
  return JSON.stringify({
    seq,
    type: 'inbox.message.created',
    subject: { kind: 'thread', id: `thread-${seq}` },
    at: '2026-08-27T10:00:00Z',
  })
}

describe('backoffDelay', () => {
  it('grows exponentially and caps at ~30s', () => {
    const schedule = [0, 1, 2, 3, 4, 5, 6, 7, 12].map((attempt) => backoffDelay(attempt, () => 0))
    // Lower bound of each window: half the ceiling.
    expect(schedule).toEqual([250, 500, 1000, 2000, 4000, 8000, 15000, 15000, 15000])
    expect(Math.max(...[0, 3, 9].map((a) => backoffDelay(a, () => 1)))).toBeLessThanOrEqual(30_000)
  })

  it('is not constant — the same attempt jitters across the window', () => {
    const low = backoffDelay(4, () => 0)
    const high = backoffDelay(4, () => 1)
    expect(high).toBeGreaterThan(low)
  })

  it('bounds jitter: never below half the ceiling, never above it', () => {
    for (const attempt of [0, 1, 2, 3, 4, 5, 6, 10]) {
      const ceiling = Math.min(30_000, 500 * 2 ** Math.min(attempt, 6))
      for (const r of [0, 0.01, 0.37, 0.5, 0.99, 1]) {
        const delay = backoffDelay(attempt, () => r)
        expect(delay).toBeGreaterThanOrEqual(ceiling / 2)
        expect(delay).toBeLessThanOrEqual(ceiling)
      }
    }
  })
})

describe('socketURL', () => {
  it('upgrades the scheme and carries the ticket and resume cursor', () => {
    const url = new URL(socketURL('tkt-abc', 42))
    expect(url.protocol).toBe('ws:')
    expect(url.pathname).toBe('/api/v1/realtime/ws')
    expect(url.searchParams.get('ticket')).toBe('tkt-abc')
    expect(url.searchParams.get('after_seq')).toBe('42')
  })

  it('omits the cursor on a first connection', () => {
    expect(new URL(socketURL('tkt-abc', 0)).searchParams.has('after_seq')).toBe(false)
  })
})

describe('requestTicket', () => {
  it('POSTs with the bearer token and returns the minted ticket', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response(JSON.stringify({ ticket: 't', expires_in: 30 }), { status: 200 }))

    await expect(requestTicket('token-9')).resolves.toEqual({ ticket: 't', expires_in: 30 })

    const call = fetchMock.mock.calls[0]
    expect(call).toBeDefined()
    expect(String(call?.[0])).toContain('/api/v1/realtime/ticket')
    expect(call?.[1]).toMatchObject({
      method: 'POST',
      credentials: 'include',
      headers: { Authorization: 'Bearer token-9' },
    })
    fetchMock.mockRestore()
  })

  it('throws a typed handshake error on refusal', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response('', { status: 401 }))
    await expect(requestTicket('token-9')).rejects.toBeInstanceOf(RealtimeHandshakeError)
    fetchMock.mockRestore()
  })
})

describe('RealtimeClient', () => {
  it('mints a ticket, connects with it and reports open', async () => {
    const h = harness()
    h.client.start()
    await h.settle()

    expect(h.sockets).toHaveLength(1)
    expect(h.sockets[0]?.url).toContain('ticket=tkt-1')
    h.sockets[0]?.open()
    expect(h.events).toEqual(['open'])
    h.client.stop()
  })

  it('forwards parsed envelopes and silently drops malformed frames', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    const socket = h.sockets[0]
    socket?.open()

    socket?.deliver(envelopeFrame(1))
    socket?.deliver('}{ not json')
    socket?.deliver(envelopeFrame(2))

    expect(h.envelopes.map((e) => e.seq)).toEqual([1, 2])
    h.client.stop()
  })

  it('reconnects on the jittered schedule and sends the last seen seq', async () => {
    let seq = 0
    const h = harness({ lastSeq: () => seq })
    h.client.start()
    await h.settle()
    h.sockets[0]?.open()
    seq = 17

    h.sockets[0]?.drop()
    expect(h.events).toEqual(['open', 'close'])
    // First retry: attempt 0, midpoint jitter of the 500ms ceiling.
    expect(h.timers.delays).toEqual([pingIntervalMs, 375])

    await h.timers.flush()
    expect(h.sockets).toHaveLength(2)
    expect(h.sockets[1]?.url).toContain('after_seq=17')
    h.client.stop()
  })

  it('backs off further on each consecutive failed connect', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    h.sockets[0]?.drop()
    const first = h.timers.delays.at(-1)

    await h.timers.flush()
    h.sockets[1]?.drop()
    const second = h.timers.delays.at(-1)

    await h.timers.flush()
    h.sockets[2]?.drop()
    const third = h.timers.delays.at(-1)

    expect(first).toBeLessThan(second ?? 0)
    expect(second).toBeLessThan(third ?? 0)
    h.client.stop()
  })

  it('resets the backoff after a connection that actually opened', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    h.sockets[0]?.drop()
    await h.timers.flush()
    h.sockets[1]?.drop()
    const escalated = h.timers.delays.at(-1) ?? 0

    await h.timers.flush()
    h.sockets[2]?.open()
    h.sockets[2]?.drop()

    expect(h.timers.delays.at(-1)).toBeLessThan(escalated)
    h.client.stop()
  })

  it('pings on the keepalive interval and clears the miss counter on a pong', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    const socket = h.sockets[0]
    socket?.open()

    await h.timers.flush()
    expect(socket?.sent).toEqual(['ping'])
    socket?.deliver('pong')
    await h.timers.flush()
    await h.timers.flush()

    // Three pings sent, every one answered or freshly counted: still open.
    expect(socket?.closes).toEqual([])
    h.client.stop()
  })

  it('drops a socket that misses two pongs', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    const socket = h.sockets[0]
    socket?.open()

    for (let i = 0; i <= missedPongLimit; i += 1) {
      // oxlint-disable-next-line no-await-in-loop -- keepalive ticks are ordered
      await h.timers.flush()
    }

    expect(socket?.sent).toEqual(['ping', 'ping'])
    expect(socket?.closes).toEqual([4000])
    h.client.stop()
  })

  it('retries a transient ticket failure instead of giving up', async () => {
    let attempts = 0
    const h = harness({
      fetchTicket: () => {
        attempts += 1
        return attempts === 1
          ? Promise.reject(new RealtimeHandshakeError(503))
          : Promise.resolve({ ticket: 'tkt-2', expires_in: 30 })
      },
    })
    h.client.start()
    await h.settle()

    expect(h.sockets).toHaveLength(0)
    expect(h.fatal).toBeNull()
    await h.timers.flush()
    await h.settle()
    expect(h.sockets).toHaveLength(1)
    h.client.stop()
  })

  it('goes fatal on a permanent refusal rather than spinning silently', async () => {
    const h = harness({ fetchTicket: () => Promise.reject(new RealtimeHandshakeError(403)) })
    h.client.start()
    await h.settle()

    expect(h.events).toEqual(['fatal'])
    expect(h.fatal).toContain('no longer have access')
    expect(h.timers.size).toBe(0)
    h.client.stop()
  })

  it('retries a 429 — the ticket route is rate-limited, not closed', async () => {
    const h = harness({ fetchTicket: () => Promise.reject(new RealtimeHandshakeError(429)) })
    h.client.start()
    await h.settle()

    expect(h.events).toEqual([])
    expect(h.timers.size).toBe(1)
    h.client.stop()
  })

  it('stop() closes the socket and cancels every pending timer', async () => {
    const h = harness()
    h.client.start()
    await h.settle()
    const socket = h.sockets[0]
    socket?.open()

    h.client.stop()

    expect(socket?.closes).toEqual([1000])
    expect(h.timers.size).toBe(0)
    // A close arriving after stop must not resurrect the connection.
    socket?.drop()
    await h.timers.flush()
    expect(h.sockets).toHaveLength(1)
  })
})
