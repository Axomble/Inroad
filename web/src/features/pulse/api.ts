// Pulse feature endpoint. The workspace pulse read-model is being built
// concurrently on the backend, so its endpoint is layered on with
// `injectEndpoints` against the frozen contract from
// docs/superpowers/specs/2026-08-04-console-redesign.md §2 — the same pattern
// the mailbox OAuth "start" endpoints use — rather than hand-editing the
// generated store/api.ts.
//
// TODO(pulse): `api/openapi.yaml` now defines GET /pulse (operationId
// `getPulse`); after the next `gen:api` regen, reconcile: re-export the
// generated types here and drop this injected endpoint for the generated hook.
// Endpoint name and URL below already match the generated output exactly.
import { api } from '@/store/api'

export type PulseSeverity = 'danger' | 'warn' | 'info'

/**
 * One server-defined attention row. `kind` is a stable machine identifier;
 * `reason` is human copy computed server-side; `href` is where fixing it
 * starts. New backend producers add rows with zero frontend changes.
 */
export interface PulseAttentionItem {
  kind: string
  severity: PulseSeverity
  count: number
  reason: string
  href: string
}

/**
 * The O(1) aggregate payload of GET /pulse. The workspace is resolved
 * server-side from the session (the house rule for data-plane routes), so the
 * request carries no workspace id.
 */
export interface WorkspacePulse {
  mailboxes: { total: number; active: number; paused: number; error: number }
  warmup: { pool: number; healthy: number; watch: number; at_risk: number }
  campaigns: { total: number; running: number; draft: number; paused: number }
  contacts: { total: number }
  sending: { sent_today: number; daily_cap: number }
  inbox: { unread: number; interested: number }
  attention: PulseAttentionItem[]
}

const pulseApi = api.injectEndpoints({
  endpoints: (build) => ({
    getPulse: build.query<WorkspacePulse, void>({
      query: () => ({ url: `/pulse` }),
    }),
  }),
})

export const { useGetPulseQuery } = pulseApi
