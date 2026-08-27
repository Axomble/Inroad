import { act, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { makeTestStore, renderWithProviders } from '@/test/render-with-providers'
import { setActiveWorkspace } from '@/store/slices/auth'
import type { RealtimeClientOptions } from '../socket-client'
import type { RealtimeEnvelope } from '../socket-events'

const mocks = vi.hoisted(() => ({
  applyEnvelopeToCache: vi.fn(() => true),
  instances: [] as Array<{ options: RealtimeClientOptions; started: number; stopped: number }>,
}))

// The transport is tested against a fake socket in socket-client.test.ts; here
// it is a spy, so this file asserts only the React binding's own behavior.
vi.mock('../socket-client', () => ({
  RealtimeClient: class {
    private readonly entry: { options: RealtimeClientOptions; started: number; stopped: number }

    constructor(options: RealtimeClientOptions) {
      this.entry = { options, started: 0, stopped: 0 }
      mocks.instances.push(this.entry)
    }

    start() {
      this.entry.started += 1
    }

    stop() {
      this.entry.stopped += 1
    }
  },
}))

vi.mock('../cache-patch', () => ({
  applyEnvelopeToCache: (...args: unknown[]) => mocks.applyEnvelopeToCache(...(args as [])),
}))

const { RealtimeProvider } = await import('../realtime-provider')
const { useRealtimeStatus } = await import('../realtime-context')

function Probe() {
  const { status, lastSeq, gapDetected } = useRealtimeStatus()
  return <output>{`${status}:${lastSeq}:${gapDetected}`}</output>
}

function mount(auth: { accessToken?: string | null; activeWorkspaceId?: string | null; userId?: string | null }) {
  const store = makeTestStore({ auth })
  const utils = renderWithProviders(
    <RealtimeProvider>
      <Probe />
    </RealtimeProvider>,
    { store },
  )
  return { ...utils, store }
}

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

function latest() {
  const entry = mocks.instances.at(-1)
  if (!entry) throw new Error('no RealtimeClient was constructed')
  return entry
}

beforeEach(() => {
  mocks.instances.length = 0
  mocks.applyEnvelopeToCache.mockClear()
})

describe('useRealtime', () => {
  it('does not connect without an access token', () => {
    mount({ accessToken: null, activeWorkspaceId: 'ws-1' })
    expect(mocks.instances).toHaveLength(0)
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('idle:0:false')
  })

  it('does not connect before a workspace is active', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: null })
    expect(mocks.instances).toHaveLength(0)
  })

  it('connects once a token and workspace are both present', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1', userId: 'user-1' })
    expect(mocks.instances).toHaveLength(1)
    expect(latest().started).toBe(1)
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('connecting:0:false')
  })

  it('publishes the live status once the socket opens', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    act(() => latest().options.onOpen())
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('live:0:false')
  })

  it('applies a frame from another user to the cache and advances the cursor', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1', userId: 'user-1' })
    act(() => latest().options.onOpen())
    act(() => latest().options.onEnvelope(envelope({ seq: 4, actor_id: 'user-2' })))

    expect(mocks.applyEnvelopeToCache).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('live:4:false')
  })

  it('DROPS a frame this user originated but still advances the cursor', () => {
    // The cursor must advance even for a dropped echo: otherwise the next
    // reconnect asks the server to replay a frame we deliberately ignored, and
    // the gap check then trips on our own decision.
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1', userId: 'user-1' })
    act(() => latest().options.onOpen())
    act(() => latest().options.onEnvelope(envelope({ seq: 4, actor_id: 'user-1' })))

    expect(mocks.applyEnvelopeToCache).not.toHaveBeenCalled()
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('live:4:false')
  })

  it('applies a worker-originated frame, which carries no actor', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1', userId: 'user-1' })
    act(() => latest().options.onOpen())
    act(() => latest().options.onEnvelope(envelope({ seq: 2, actor_id: null })))
    expect(mocks.applyEnvelopeToCache).toHaveBeenCalledTimes(1)
  })

  it('reports the last applied seq to the transport for replay', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    act(() => latest().options.onOpen())
    act(() => latest().options.onEnvelope(envelope({ seq: 31 })))
    expect(latest().options.lastSeq()).toBe(31)
  })

  it('surfaces a gap so the indicator can tell the user their view is incomplete', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    act(() => latest().options.onOpen())
    act(() => latest().options.onEnvelope(envelope({ seq: 1 })))
    act(() => latest().options.onEnvelope(envelope({ seq: 9 })))
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('live:9:true')
  })

  it('reports reconnecting on a transport close and offline on a fatal', () => {
    mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    act(() => latest().options.onClose())
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('reconnecting:0:false')
    act(() => latest().options.onFatal('gone'))
    expect(screen.getByRole('status', { hidden: true }).textContent).toBe('offline:0:false')
  })

  it('stops the socket on unmount', () => {
    const { unmount } = mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    const entry = latest()
    unmount()
    expect(entry.stopped).toBe(1)
  })

  it('opens a new socket on a workspace switch, because the ticket pins the workspace', () => {
    const { store } = mount({ accessToken: 'tok', activeWorkspaceId: 'ws-1' })
    const first = latest()
    act(() => {
      store.dispatch(
        setActiveWorkspace({ activeWorkspaceId: 'ws-2', role: 'owner', accessToken: 'tok-2' }),
      )
    })
    expect(first.stopped).toBe(1)
    expect(mocks.instances).toHaveLength(2)
    expect(latest().options.token).toBe('tok-2')
  })
})
