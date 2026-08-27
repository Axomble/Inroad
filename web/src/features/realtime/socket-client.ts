import { config } from '@/lib/config'
import { parseEnvelope, type RealtimeEnvelope } from './socket-events'

/**
 * The realtime transport. Ticket handshake, connect, keepalive, backoff — and no
 * React, so the whole lifecycle is testable against a fake socket.
 *
 * Mirrors `features/agent/stream-client.ts` in spirit: the SSE client is opened
 * with `fetch` and a real `Authorization` header, which a browser WebSocket
 * cannot carry. Hence the two-step handshake (spec §3): POST for a 30s
 * single-use ticket, then connect with it in the query string.
 */

/** A handshake request that reached the server and was refused. */
export class RealtimeHandshakeError extends Error {
  readonly status: number

  constructor(status: number) {
    super(`Realtime ticket request returned HTTP ${status}.`)
    this.name = 'RealtimeHandshakeError'
    this.status = status
  }
}

export interface RealtimeTicket {
  ticket: string
  expires_in: number
}

/** The subset of WebSocket this client uses, so a test can supply a fake. */
export interface SocketLike {
  send(data: string): void
  close(code?: number, reason?: string): void
  onopen: ((event: unknown) => void) | null
  onmessage: ((event: { data: unknown }) => void) | null
  onclose: ((event: unknown) => void) | null
  onerror: ((event: unknown) => void) | null
}

export interface RealtimeClientOptions {
  /** Bearer token for minting the ticket. */
  token: string
  /** Highest seq already applied; the server replays from here. */
  lastSeq: () => number
  onOpen: () => void
  onEnvelope: (envelope: RealtimeEnvelope) => void
  /** A transport-level close. The client has already scheduled the retry. */
  onClose: () => void
  /** Terminal: retrying cannot help. Carries copy for the indicator. */
  onFatal: (message: string) => void
  /** Injection seams. Defaults are the real browser APIs. */
  createSocket?: (url: string) => SocketLike
  fetchTicket?: (token: string) => Promise<RealtimeTicket>
  setTimeout?: (fn: () => void, ms: number) => number
  clearTimeout?: (handle: number) => void
  random?: () => number
}

/** Ping cadence and the miss budget (spec §4: drop after two missed pongs). */
export const pingIntervalMs = 30_000
export const missedPongLimit = 2

const baseBackoffMs = 500
const maxBackoffMs = 30_000

/**
 * Exponential backoff with jitter, capped at ~30s (spec §6).
 *
 * The jitter is full-range on the *upper* half of the window rather than a plain
 * `delay * random()`: an unjittered schedule reconnects every tab of every
 * workspace at the same instant after an API restart, which is the thundering
 * herd this exists to avoid, while a jitter that can return ~0 would let a tab
 * hammer a server that is still down.
 */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(maxBackoffMs, baseBackoffMs * 2 ** Math.min(attempt, 6))
  return Math.round(ceiling / 2 + (ceiling / 2) * random())
}

function apiURL(path: string): string {
  const base = config.apiBaseUrl
  const normalized = base.endsWith('/') ? base.slice(0, -1) : base
  return new URL(`${normalized}${path}`, window.location.origin).toString()
}

/** `https:` -> `wss:`, `http:` -> `ws:`, preserving host, port and path. */
export function socketURL(ticket: string, lastSeq: number): string {
  const url = new URL(apiURL('/realtime/ws'))
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  url.searchParams.set('ticket', ticket)
  if (lastSeq > 0) url.searchParams.set('after_seq', String(lastSeq))
  return url.toString()
}

export async function requestTicket(token: string): Promise<RealtimeTicket> {
  const response = await fetch(apiURL('/realtime/ticket'), {
    method: 'POST',
    headers: { Accept: 'application/json', Authorization: `Bearer ${token}` },
    credentials: 'include',
  })
  if (!response.ok) throw new RealtimeHandshakeError(response.status)
  return (await response.json()) as RealtimeTicket
}

/**
 * A 4xx from the ticket endpoint is the server saying this will never work
 * (session gone, no workspace, rate-limited past the point of usefulness); 408
 * and 5xx are transient. Same classification as `use-agent-stream.ts:59`, and
 * for the same reason: a silent reconnect spin against a permanent refusal is
 * indistinguishable from a quiet workspace.
 */
function isRetryable(error: unknown): boolean {
  if (!(error instanceof RealtimeHandshakeError)) return true
  if (error.status === 408 || error.status === 429) return true
  return error.status < 400 || error.status >= 500
}

function fatalMessage(error: unknown): string {
  if (!(error instanceof RealtimeHandshakeError)) return 'Live updates are unavailable. Reload the page to reconnect.'
  switch (error.status) {
    case 401:
      return 'Your session expired. Reload the page to resume live updates.'
    case 403:
      return 'You no longer have access to live updates for this workspace.'
    default:
      return `Live updates were refused (HTTP ${error.status}). Reload the page to retry.`
  }
}

/**
 * One socket, one workspace, one lifetime. `start()` runs until `stop()`;
 * every close schedules a jittered reconnect unless the failure was terminal.
 */
export class RealtimeClient {
  private readonly options: RealtimeClientOptions
  private socket: SocketLike | null = null
  private stopped = false
  private attempt = 0
  private retryHandle: number | null = null
  private pingHandle: number | null = null
  private missedPongs = 0

  constructor(options: RealtimeClientOptions) {
    this.options = options
  }

  start(): void {
    if (this.stopped) return
    void this.connect()
  }

  stop(): void {
    this.stopped = true
    this.clearTimer('retryHandle')
    this.clearTimer('pingHandle')
    const { socket } = this
    this.socket = null
    if (socket) {
      socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null
      socket.close(1000, 'client stopped')
    }
  }

  private get timers() {
    return {
      set: this.options.setTimeout ?? ((fn: () => void, ms: number) => window.setTimeout(fn, ms)),
      clear: this.options.clearTimeout ?? ((handle: number) => window.clearTimeout(handle)),
    }
  }

  private clearTimer(field: 'retryHandle' | 'pingHandle'): void {
    const handle = this[field]
    if (handle !== null) {
      this.timers.clear(handle)
      this[field] = null
    }
  }

  private async connect(): Promise<void> {
    if (this.stopped) return
    const fetcher = this.options.fetchTicket ?? requestTicket
    let ticket: RealtimeTicket
    try {
      ticket = await fetcher(this.options.token)
    } catch (error) {
      if (this.stopped) return
      if (!isRetryable(error)) {
        this.options.onFatal(fatalMessage(error))
        return
      }
      this.scheduleReconnect()
      return
    }
    if (this.stopped) return
    this.openSocket(ticket.ticket)
  }

  private openSocket(ticket: string): void {
    const factory =
      this.options.createSocket ?? ((url: string) => new WebSocket(url) as unknown as SocketLike)
    const socket = factory(socketURL(ticket, this.options.lastSeq()))
    this.socket = socket

    socket.onopen = () => {
      if (this.stopped || this.socket !== socket) return
      this.attempt = 0
      this.missedPongs = 0
      this.options.onOpen()
      this.schedulePing()
    }

    socket.onmessage = (event) => {
      if (this.stopped || this.socket !== socket) return
      // A pong is a control frame the server echoes as text; it clears the miss
      // counter and is never an envelope.
      if (event.data === 'pong' || event.data === '{"type":"pong"}') {
        this.missedPongs = 0
        return
      }
      const envelope = parseEnvelope(event.data)
      if (envelope) this.options.onEnvelope(envelope)
    }

    socket.onerror = () => {
      // `onclose` always follows `onerror`; retrying here too would double the
      // reconnect rate.
    }

    socket.onclose = () => {
      if (this.stopped || this.socket !== socket) return
      this.socket = null
      this.clearTimer('pingHandle')
      this.options.onClose()
      this.scheduleReconnect()
    }
  }

  /**
   * Keepalive. Each tick counts an unanswered ping; the second unanswered one
   * closes the socket, because an idle-timeout intermediary leaves a half-open
   * connection that looks perfectly alive from here (spec §4).
   */
  private schedulePing(): void {
    this.clearTimer('pingHandle')
    this.pingHandle = this.timers.set(() => {
      this.pingHandle = null
      const socket = this.socket
      if (this.stopped || !socket) return
      if (this.missedPongs >= missedPongLimit) {
        // Drop it and let the close handler reconnect. Do not clear `socket`
        // first — the close handler is what schedules the retry.
        socket.close(4000, 'pong timeout')
        return
      }
      this.missedPongs += 1
      socket.send('ping')
      this.schedulePing()
    }, pingIntervalMs)
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.retryHandle !== null) return
    const delay = backoffDelay(this.attempt, this.options.random ?? Math.random)
    this.attempt += 1
    this.retryHandle = this.timers.set(() => {
      this.retryHandle = null
      void this.connect()
    }, delay)
  }
}
