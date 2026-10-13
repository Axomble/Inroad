import { createContext, useCallback, useContext } from 'react'
import type { ConditionDraft } from './branch-draft'
import type { StepWithId } from './step-card'

/**
 * What the canvas's side panel shows: one step's editor, a new step's anchored
 * after a step (`null` = first), or the condition editor for the router on one
 * step (new or existing). Owned by the sequence editor, so its
 * section-bar "Add step" opens the same panel as an edge's "+" and the two can
 * never both be open. `returnFocus` is whatever had focus when the panel
 * opened; closing without a better target hands focus back to it.
 */
export type SequencePanel =
  | { kind: 'edit'; stepId: string; returnFocus: HTMLElement | null }
  | { kind: 'add'; afterId: string | null; returnFocus: HTMLElement | null }
  | {
      kind: 'condition'
      stepId: string
      returnFocus: HTMLElement | null
      /** Opens the editor on this draft rather than the saved branch (a drag the rules refused). */
      draft?: ConditionDraft
    }

/** The element that currently has focus, for `SequencePanel.returnFocus`. */
export function currentFocus(): HTMLElement | null {
  return document.activeElement instanceof HTMLElement ? document.activeElement : null
}

export type MoveBlock = { reason: string | null }

/** A node control that can be asked for focus: `${stepId}:${control}`. */
export type StepControl = 'edit' | 'up' | 'down' | 'add-condition' | 'condition'
export const focusKey = (stepId: string, control: StepControl) => `${stepId}:${control}`

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
  /** Opens the condition editor for the router on `stepId` (creating one if it has none). */
  editCondition: (stepId: string) => void
  /** The step whose condition editor is open. */
  editingConditionStepId: string | null
  openVariants: (step: StepWithId, position: number) => void
  requestDelete: (step: StepWithId) => void
  moveStep: (stepId: string, delta: -1 | 1) => void
  /**
   * `null` when the move is allowed. Otherwise the move is refused before it is
   * sent, with a `reason` when there is one worth saying (off the end of the
   * sequence needs none).
   */
  moveBlock: (stepId: string, delta: -1 | 1) => MoveBlock | null
  /** A reorder is in flight; move controls wait for it rather than race it. */
  isReordering: boolean
  /** The control that should take focus next (after a move, after a save). */
  focusRequest: string | null
  clearFocusRequest: () => void
}

export const SequenceCanvasActionsContext = createContext<SequenceCanvasActions | null>(null)

export function useSequenceCanvasActions(): SequenceCanvasActions {
  const actions = useContext(SequenceCanvasActionsContext)
  if (!actions) throw new Error('sequence canvas nodes must render inside SequenceCanvasActionsContext')
  return actions
}

/**
 * A ref that focuses its element when the canvas asks for `key`. A callback
 * ref rather than an effect, because the control is often not there yet when
 * the request is made — the button a save creates only mounts once the refetch
 * lands — and a callback ref runs when the element attaches, whenever that is.
 * Waits while the control is disabled (a reorder in flight disables every move
 * button): the ref is re-created when it becomes usable, and runs again.
 */
export function useFocusRequest<E extends HTMLElement>(key: string, disabled = false) {
  const { focusRequest, clearFocusRequest } = useSequenceCanvasActions()
  const wanted = focusRequest === key && !disabled
  return useCallback(
    (element: E | null) => {
      if (!wanted || !element) return
      element.focus()
      clearFocusRequest()
    },
    [clearFocusRequest, wanted],
  )
}
