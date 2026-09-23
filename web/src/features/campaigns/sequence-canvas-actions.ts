import { createContext, useContext } from 'react'
import type { StepWithId } from './step-card'

/**
 * What a node on the sequence canvas can ask for. Carried by context instead of
 * each node's `data` so the graph stays plain data: a re-created handler must
 * not change the graph's identity and trigger a re-layout.
 */
export type SequenceCanvasActions = {
  campaignId: string
  /** The step whose editor is open, so its node can show it's the one being edited. */
  editingStepId: string | null
  editStep: (stepId: string) => void
  openVariants: (step: StepWithId, position: number) => void
  requestDelete: (step: StepWithId) => void
  moveStep: (stepId: string, delta: -1 | 1) => void
  /** A reorder is in flight; move controls wait for it rather than race it. */
  isReordering: boolean
}

export const SequenceCanvasActionsContext = createContext<SequenceCanvasActions | null>(null)

export function useSequenceCanvasActions(): SequenceCanvasActions {
  const actions = useContext(SequenceCanvasActionsContext)
  if (!actions) throw new Error('sequence canvas nodes must render inside SequenceCanvasActionsContext')
  return actions
}
