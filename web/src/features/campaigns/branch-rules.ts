import { useMemo } from 'react'
// Read-only cross-feature hook (the documented exception): which reply labels
// exist, and which of them stop the sequence, belongs to the reply-labels
// feature. Hooks only — no reply-labels UI or state is imported.
import { useListReplyLabelsQuery } from '@/features/reply-labels/api'
import { useGetCampaignQuery } from './api'
import type { RouteableLabel } from './branch-draft'

export type BranchRules = {
  /** `undefined` while unknown: the check is then left to the server. */
  trackingEnabled: boolean | undefined
  /** `undefined` while loading. */
  replyLabels: RouteableLabel[] | undefined
  labelsFailed: boolean
}

/**
 * The campaign-wide half of what `validateDraft` checks a condition against —
 * shared by the condition editor and by an exit drag, which is validated the
 * same way before anything is sent. (Per-step HTML is the editor's to add.)
 */
export function useBranchRules(campaignId: string): BranchRules {
  const { data: campaign } = useGetCampaignQuery({ id: campaignId })
  const labelsQuery = useListReplyLabelsQuery()
  const replyLabels = useMemo(
    () =>
      labelsQuery.data?.labels.map((label) => ({
        key: label.key,
        label: label.label,
        stopsEnrollment: label.stops_enrollment,
      })),
    [labelsQuery.data],
  )
  return { trackingEnabled: campaign?.tracking_enabled, replyLabels, labelsFailed: labelsQuery.isError }
}
