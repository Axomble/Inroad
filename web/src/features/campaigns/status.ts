/** Map a campaign's backend status to a StatusPill tone + label. */
export type CampaignTone = 'draft' | 'running' | 'paused' | 'done'

export function campaignTone(status?: string): CampaignTone {
  switch (status) {
    case 'running':
      return 'running'
    case 'paused':
      return 'paused'
    case 'done':
      return 'done'
    default:
      return 'draft'
  }
}

export function campaignLabel(status?: string): string {
  switch (status) {
    case 'running':
      return 'Running'
    case 'paused':
      return 'Paused'
    case 'done':
      return 'Done'
    default:
      return 'Draft'
  }
}
