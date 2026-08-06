// Shared pause/resume behaviour, kept out of `lifecycle-menu.tsx` because it's
// a hook (not a component) and co-exporting hooks with components from one
// file trips `react/only-export-components`'s fast-refresh warning.
import { useState } from 'react'
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import type { Campaign } from '@/store/api'
import { usePauseCampaignMutation, useResumeCampaignMutation } from './api'

/** The `{"error": "…"}` envelope the API writes, read through the typed seam. */
function serverReason(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const reason = (error.data as { error?: string } | undefined)?.error
  return typeof reason === 'string' && reason.trim() !== '' ? reason : undefined
}

function pastTense(action: 'pause' | 'resume' | 'delete'): string {
  if (action === 'pause') return 'paused'
  if (action === 'resume') return 'resumed'
  return 'deleted'
}

/**
 * Copy for a failed pause/resume/delete. A 409 here is a lifecycle-guard
 * rejection ("campaign is not running") that only the API can phrase
 * correctly for the status it actually saw, so its own reason wins over a
 * guess at the exact wording.
 */
export function lifecycleErrorMessage(action: 'pause' | 'resume' | 'delete', error: unknown): string {
  const status = httpStatus(error)
  const reason = serverReason(error)
  if (status === 409) return reason ?? `This campaign can't be ${pastTense(action)} from its current status.`
  if (status === 404) return 'This campaign no longer exists — refresh the page.'
  return `Couldn't ${action} this campaign. Please try again.`
}

/**
 * Shared pause/resume behaviour: the mutation triggers, the confirm gate that
 * pause (but not resume) requires, and the resulting inline error.
 *
 * Call this exactly **once per campaign row/topbar** — never once per trigger
 * control. A campaign can have two controls that can pause/resume it at once
 * (the detail page's dedicated button and `LifecycleMenu`'s own menu item);
 * if each called this independently, each would get its own mutation trigger
 * and its own confirm-dialog state, so the two confirm dialogs could stack and
 * each fire its own `POST /pause`. The caller computes one instance and passes
 * it to every control (and to the one `PauseResumeDialog`) that needs it.
 */
export function usePauseResume(campaign: Campaign) {
  const id = campaign.id ?? ''
  const [pause, pauseState] = usePauseCampaignMutation()
  const [resume, resumeState] = useResumeCampaignMutation()
  const [confirmPause, setConfirmPause] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onPause() {
    setError(null)
    const res = await pause({ id })
    setConfirmPause(false)
    if ('error' in res) setError(lifecycleErrorMessage('pause', res.error))
  }

  async function onResume() {
    setError(null)
    const res = await resume({ id })
    if ('error' in res) setError(lifecycleErrorMessage('resume', res.error))
  }

  return {
    confirmPause,
    setConfirmPause,
    onPause,
    onResume,
    isPausing: pauseState.isLoading,
    isResuming: resumeState.isLoading,
    error,
  }
}

export type PauseResumeController = ReturnType<typeof usePauseResume>
