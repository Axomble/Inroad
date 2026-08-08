import { useState } from 'react'
import { z } from 'zod'
import { Loader2, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useCrmMoveDealMutation } from './api'
import type { CrmEvent } from '@/features/records/api'
import { crmErrorMessage } from './error-copy'

const stageChangeDataSchema = z.object({ from_stage_id: z.string().uuid() }).passthrough()

/**
 * Undo for a `deal.stage_changed` event, rendered into the activity feed's action
 * slot.
 *
 * It lives in `features/crm` rather than in the shared activity panel because
 * everything it touches is deal-specific: the event's `data` shape, the deal move
 * mutation, and the CRM error copy. A record-generic feed cannot own any of that,
 * and the panel is used by contacts too.
 *
 * Returns nothing for any other event, so the caller can hand it every row.
 */
export function RevertStageChange({ dealId, event }: { dealId: string; event: CrmEvent }) {
  const [moveDeal, moveState] = useCrmMoveDealMutation()
  const [error, setError] = useState<string | null>(null)
  // `data` is an open JSON object in the contract, so the boundary is parsed once
  // before its fields reach a mutation.
  const previousStage = event.name === 'deal.stage_changed' ? stageChangeDataSchema.safeParse(event.data) : null
  if (previousStage?.success !== true) return null

  const revert = async () => {
    setError(null)
    try {
      await moveDeal({ id: dealId, crmMoveDealInput: { stage_id: previousStage.data.from_stage_id } }).unwrap()
    } catch (failure) {
      setError(crmErrorMessage(failure, 'The stage change could not be reverted.'))
    }
  }

  return (
    <div className="min-w-0">
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => void revert()}
        disabled={moveState.isLoading}
        aria-label="Revert this stage change"
      >
        {moveState.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCcw aria-hidden="true" />}
        Revert
      </Button>
      {error ? <p role="alert" className="mt-2 text-xs text-danger">{error}</p> : null}
    </div>
  )
}
