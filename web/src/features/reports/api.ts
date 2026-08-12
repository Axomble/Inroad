// The report contract lives in `api/openapi.yaml` and is generated into
// `store/api.ts`; this file only re-exports it under the feature's names.
// One source of truth: never re-declare these shapes by hand.
import type { CampaignPerformance, CampaignReport } from '@/store/api'

export { useGetCampaignReportQuery } from '@/store/api'

/** Every campaign's lifetime performance plus the workspace roll-up. */
export type WorkspaceCampaignReport = CampaignReport

/** One campaign's row in the comparison. */
export type CampaignRow = CampaignPerformance
