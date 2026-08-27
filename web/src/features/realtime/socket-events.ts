/**
 * Wire envelope + the pure part of the transport.
 *
 * Mirrors `features/agent/stream-state.ts`: this module knows the shape of what
 * the server sends and how a sequence of frames folds into client state, and
 * nothing about sockets or React. That is what lets the ordering, duplicate and
 * gap rules be tested as arithmetic instead of against a live connection.
 */

/** The event types this client understands (spec §5, "Events — current"). */
export type RealtimeEventType =
  | 'inbox.message.created'
  | 'inbox.reply.classified'
  | 'send.bounced'
  | 'campaign.launched'
  | 'mailbox.changed'
  | 'deal.moved'
  | 'pulse.updated'

/**
 * What an envelope's `type` may be on the wire.
 *
 * Deliberately widened past `RealtimeEventType`: the server may ship a new event
 * before this client learns it (the envelope is versionless by design, spec
 * §5), and a `data` payload is opaque for the same reason. Narrow with
 * `isKnownEventType` at the point of handling; never assume the union.
 */
export interface RealtimeEnvelope {
  /** Monotonic per workspace. Drives replay and the gap check. */
  seq: number
  type: RealtimeEventType | (string & {})
  subject: { kind: string; id: string }
  at: string
  /** Who caused this. Present so a client can drop its own echoes (spec §6). */
  actor_id?: string | null
  data?: Record<string, unknown>
}

export type ConnectionStatus = 'idle' | 'connecting' | 'live' | 'reconnecting' | 'offline'

export interface RealtimeState {
  status: ConnectionStatus
  /** Highest applied seq. Sent on reconnect so the server replays from here. */
  lastSeq: number
  /**
   * True when a frame arrived with `seq > lastSeq + 1` — the replay window was
   * missed, so caches patched from here on are known-incomplete and the UI
   * should do a full refetch rather than trust its patches.
   */
  gapDetected: boolean
  /** Consecutive failed connects; drives the backoff schedule. */
  attempt: number
  /** Terminal, human-readable reason. Set only when reconnecting is pointless. */
  error: string | null
}

export const initialRealtimeState: RealtimeState = {
  status: 'idle',
  lastSeq: 0,
  gapDetected: false,
  attempt: 0,
  error: null,
}

/**
 * What the reducer folds. Frames and lifecycle transitions share one type so
 * the whole connection is one state machine with one test surface.
 */
export type RealtimeAction =
  | { kind: 'connecting' }
  | { kind: 'open' }
  | { kind: 'frame'; envelope: RealtimeEnvelope }
  | { kind: 'closed' }
  | { kind: 'failed'; error: string }
  | { kind: 'resumed' }
  | { kind: 'reset' }

/**
 * `(state, action) => state`, pure and total.
 *
 * The three rules that matter, and why:
 *
 * - **Out-of-order / duplicate frames are ignored, not applied.** Reconnect
 *   replays from `lastSeq`, and the server subscribes before reading its log
 *   (`stream.go:217`), so overlap at the seam is expected and normal. Applying a
 *   replayed frame twice would double-count an unread badge.
 * - **A gap latches.** Once the replay window is missed the client cannot know
 *   what it lost, so the flag stays set until a deliberate `resumed` (the
 *   consumer having done its full refetch) or `reset`.
 * - **An unknown `type` still advances `lastSeq`.** It is a real event the client
 *   simply cannot render; treating it as a gap would put every client into
 *   permanent refetch mode the moment the server ships a new event type.
 */
export function realtimeReducer(state: RealtimeState, action: RealtimeAction): RealtimeState {
  switch (action.kind) {
    case 'connecting':
      return { ...state, status: state.lastSeq > 0 ? 'reconnecting' : 'connecting', error: null }
    case 'open':
      return { ...state, status: 'live', attempt: 0, error: null }
    case 'frame': {
      const { seq } = action.envelope
      if (!Number.isSafeInteger(seq) || seq <= state.lastSeq) return state
      const gap = state.lastSeq > 0 && seq > state.lastSeq + 1
      return { ...state, lastSeq: seq, gapDetected: state.gapDetected || gap }
    }
    case 'closed':
      return { ...state, status: 'reconnecting', attempt: state.attempt + 1 }
    case 'failed':
      return { ...state, status: 'offline', error: action.error }
    case 'resumed':
      return { ...state, gapDetected: false }
    case 'reset':
      return { ...initialRealtimeState }
  }
}

/**
 * Parse a raw socket message. Returns null for anything that is not a usable
 * envelope — a malformed frame must never throw inside an `onmessage` handler,
 * because there is nobody to catch it and the socket would be left half-dead.
 */
export function parseEnvelope(raw: unknown): RealtimeEnvelope | null {
  if (typeof raw !== 'string') return null
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof value !== 'object' || value === null) return null
  const candidate = value as Partial<RealtimeEnvelope>
  if (!Number.isSafeInteger(candidate.seq) || (candidate.seq ?? 0) < 1) return null
  if (typeof candidate.type !== 'string' || !candidate.type) return null
  const subject = candidate.subject
  if (typeof subject?.kind !== 'string' || typeof subject.id !== 'string') return null
  return {
    seq: candidate.seq as number,
    type: candidate.type,
    subject: { kind: subject.kind, id: subject.id },
    at: typeof candidate.at === 'string' ? candidate.at : '',
    actor_id: typeof candidate.actor_id === 'string' ? candidate.actor_id : null,
    data: typeof candidate.data === 'object' && candidate.data !== null ? candidate.data : {},
  }
}

/**
 * True when this envelope describes something this tab just did, and so must be
 * dropped. `crm/api.ts:148` already applied an optimistic patch for the actor's
 * own drag; re-applying the server's echo on top makes the deal visibly snap
 * back and forth (spec §6).
 *
 * An envelope with no actor (a worker-originated event — a bounce, a poll
 * result) is never a self-echo.
 */
export function isSelfEcho(envelope: RealtimeEnvelope, selfActorId: string | null | undefined): boolean {
  if (!selfActorId || !envelope.actor_id) return false
  return envelope.actor_id === selfActorId
}
