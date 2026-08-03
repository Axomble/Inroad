// The pulse contract now lives in `api/openapi.yaml` and is generated into
// `store/api.ts` (`getPulse`); this file only re-exports it under the
// feature's names — the reconciliation the original injected endpoint's TODO
// promised. One source of truth: never re-declare these shapes by hand.
import type { Pulse, PulseAttention } from '@/store/api'

export { useGetPulseQuery } from '@/store/api'

/**
 * The O(1) aggregate payload of GET /pulse. The workspace is resolved
 * server-side from the session (the house rule for data-plane routes), so the
 * request carries no workspace id.
 */
export type WorkspacePulse = Pulse

/**
 * One server-defined attention row. `kind` is a stable machine identifier;
 * `reason` is human copy computed server-side; `href` is where fixing it
 * starts. New backend producers add rows with zero frontend changes.
 */
export type PulseAttentionItem = PulseAttention

export type PulseSeverity = PulseAttention['severity']
