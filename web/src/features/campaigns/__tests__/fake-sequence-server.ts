// A small stateful stand-in for the sequence endpoints, installed as `fetch`.
// Reorder, create, delete and the branch writes change its state, and every
// GET answers from that state, so a refetch is consistent with the mutations
// before it — the canvas is exercised through RTK Query exactly as in the app.
// Not a test file itself: shared by the canvas suites.
import { vi } from 'vitest'
import type { StepBranch, StepBranchRequest } from '../api'

export type FakeStep = {
  id: string
  step_order: number
  delay_seconds: number
  subject: string
  body_text?: string
  body_html?: string
}
export type CapturedRequest = { method: string; url: string; body: unknown }
export type FakeReplyLabel = { key: string; label: string; stops_enrollment: boolean }

export type FakeSequenceServer = {
  steps: FakeStep[]
  /** Branches by source step. */
  branches: Map<string, StepBranch>
  requests: CapturedRequest[]
  campaign: { id: string; status: string; tracking_enabled: boolean }
  replyLabels: FakeReplyLabel[]
  /** When set, the reorder endpoint answers with this instead of reordering. */
  reorderFails: Response | null
  /** When set, a branch write (PUT or DELETE) answers with this instead of saving. */
  branchFails: Response | null
  /** When true, GET /graph answers 500. */
  graphFails: boolean
  /** When set, GET /steps waits on it — "the refetch hasn't come back yet". */
  listGate: Promise<void> | null
}

export function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } })
}

function renumber(list: FakeStep[]): FakeStep[] {
  return list.map((step, index) => ({ ...step, step_order: index + 1 }))
}

function graphOf(server: FakeSequenceServer) {
  return {
    campaign_id: server.campaign.id,
    entry_step_id: server.steps[0]?.id ?? null,
    nodes: server.steps.map((step, index) => ({
      step_id: step.id,
      step_order: step.step_order,
      default_next_step_id: server.steps[index + 1]?.id ?? null,
      branch: server.branches.get(step.id) ?? null,
    })),
  }
}

function labelList(server: FakeSequenceServer) {
  return {
    labels: server.replyLabels.map((label, index) => ({
      id: `rl-${label.key}`,
      key: label.key,
      label: label.label,
      color: '#888888',
      position: index,
      is_builtin: true,
      stops_enrollment: label.stops_enrollment,
      is_automated: false,
      suppresses_contact: false,
      captures_deal: false,
      defers_enrollment: false,
      created_at: '2026-09-01T00:00:00Z',
      updated_at: '2026-09-01T00:00:00Z',
    })),
  }
}

export function installFakeSequenceServer(steps: FakeStep[]): FakeSequenceServer {
  const server: FakeSequenceServer = {
    steps,
    branches: new Map(),
    requests: [],
    campaign: { id: 'c-1', status: 'draft', tracking_enabled: true },
    replyLabels: [],
    reorderFails: null,
    branchFails: null,
    graphFails: false,
    listGate: null,
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      const text = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      const body = text ? (JSON.parse(text) as unknown) : undefined
      server.requests.push({ method, url, body })

      if (url.endsWith('/reply-labels')) return jsonResponse(labelList(server))
      if (url.endsWith('/graph')) {
        return server.graphFails ? jsonResponse({ error: 'boom' }, 500) : jsonResponse(graphOf(server))
      }
      const branchPath = /\/steps\/([^/]+)\/branch$/.exec(url)
      if (branchPath?.[1]) {
        if (server.branchFails) return server.branchFails
        const stepId = branchPath[1]
        if (method === 'DELETE') {
          server.branches.delete(stepId)
          return new Response(null, { status: 204 })
        }
        const request = body as StepBranchRequest
        const saved: StepBranch = {
          step_id: stepId,
          condition: request.condition,
          within_days: request.within_days ?? null,
          reply_label_key: request.reply_label_key ?? null,
          yes_step_id: request.yes_step_id ?? null,
          no_step_id: request.no_step_id ?? null,
          updated_at: '2026-09-23T00:00:00Z',
        }
        server.branches.set(stepId, saved)
        return jsonResponse(saved)
      }
      if (url.endsWith('/steps/s-1/variants')) {
        return jsonResponse([
          { id: 'v-1', step_id: 's-1', label: 'B', weight: 50, subject: '', body_text: '', body_html: '<p>b</p>' },
          { id: 'v-2', step_id: 's-1', label: 'C', weight: 50, subject: '', body_text: '', body_html: '<p>c</p>' },
        ])
      }
      if (url.endsWith('/variants')) return jsonResponse([])
      if (url.endsWith('/steps/reorder')) {
        if (server.reorderFails) return server.reorderFails
        const { step_ids } = body as { step_ids: string[] }
        server.steps = renumber(step_ids.flatMap((id) => server.steps.filter((step) => step.id === id)))
        return jsonResponse(server.steps)
      }
      if (/\/steps\/[^/]+$/.test(url) && method === 'PUT') return jsonResponse(server.steps[0])
      if (/\/steps\/[^/]+$/.test(url) && method === 'DELETE') {
        const id = url.split('/').at(-1)
        server.steps = renumber(server.steps.filter((step) => step.id !== id))
        return new Response(null, { status: 204 })
      }
      if (url.endsWith('/steps') && method === 'POST') {
        const created: FakeStep = {
          id: 's-new',
          step_order: server.steps.length + 1,
          delay_seconds: 0,
          subject: (body as { subject?: string }).subject ?? '',
        }
        server.steps = [...server.steps, created]
        return jsonResponse(created)
      }
      if (url.endsWith('/steps')) {
        if (server.listGate) await server.listGate
        return jsonResponse(server.steps)
      }
      if (url.endsWith(`/campaigns/${server.campaign.id}`)) return jsonResponse(server.campaign)
      return jsonResponse({ error: `unhandled ${method} ${url}` }, 404)
    }),
  )
  return server
}

export function lastRequest(
  server: FakeSequenceServer,
  predicate: (request: CapturedRequest) => boolean,
): CapturedRequest | undefined {
  return [...server.requests].reverse().find(predicate)
}

/** The bodies sent to URLs ending in `suffix`, in order. */
export function bodiesTo(server: FakeSequenceServer, suffix: string, method?: string): unknown[] {
  return server.requests
    .filter((request) => request.url.endsWith(suffix) && (method === undefined || request.method === method))
    .map((request) => request.body)
}
