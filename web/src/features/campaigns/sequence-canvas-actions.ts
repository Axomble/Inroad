import { createContext, useContext, useEffect, useRef } from 'react'
import type { StepWithId } from './step-card'

/**
 * What the canvas's side panel shows: one step's editor, or a new step's
 * anchored after a step (`null` = first). Owned by the sequence editor, so its
 * section-bar "Add step" opens the same panel as an edge's "+" and the two can
 * never both be open. `returnFocus` is whatever had focus when the panel
 * opened; closing without a better target hands focus back to it.
 */
export type SequencePanel =
  | { kind: 'edit'; stepId: string; returnFocus: HTMLElement | null }
  | { kind: 'add'; afterId: string | null; returnFocus: HTMLElement | null }

/** The element that currently has focus, for `SequencePanel.returnFocus`. */
export function currentFocus(): HTMLElement | null {
  return document.activeElement instanceof HTMLElement ? document.activeElement : null
}

export type MoveBlock = { reason: string | null }

/** A node control that can be asked for focus: `${stepId}:${control}`. */
export type StepControl = 'edit' | 'up' | 'down'
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
 * Focuses the returned element when the canvas asks for `key`. Waits while the
 * control is disabled (a reorder in flight disables every move button), so a
 * request made at click time lands once the control is usable again.
 */
export function useFocusRequest<E extends HTMLElement>(key: string, disabled = false) {
  const ref = useRef<E>(null)
  const { focusRequest, clearFocusRequest } = useSequenceCanvasActions()
  useEffect(() => {
    if (focusRequest !== key || disabled || !ref.current) return
    ref.current.focus()
    clearFocusRequest()
  }, [clearFocusRequest, disabled, focusRequest, key])
  return ref
}
