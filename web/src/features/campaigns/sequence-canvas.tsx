import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Connection, Edge } from '@xyflow/react'
import { FlowCanvas } from '@/components/shared/flow/flow-canvas'
import { flowContentHeight, useFlowLayout } from '@/components/shared/flow/flow-layout'
import type { FlowInsertTarget } from '@/components/shared/flow/flow-insert-context'
import { useReorderStepsMutation, type SequenceStep } from './api'
import {
  SequenceCanvasActionsContext,
  currentFocus,
  focusKey,
  type MoveBlock,
  type SequenceCanvasActions,
  type SequencePanel,
} from './sequence-canvas-actions'
import {
  START_NODE_ID,
  applyOrder,
  buildSequenceGraph,
  isMeaningfulConnection,
  leadsWithSubject,
  moveStep,
  orderChanged,
  orderForConnection,
  placeAfter,
} from './sequence-graph'
import { sequenceNodeRegistry } from './sequence-node-registry'
import type { StepWithId } from './step-card'
import { reorderErrorMessage } from './step-error'
import { StepForm } from './step-form'

const NEEDS_SUBJECT_FIRST = 'The first step opens the thread, so it needs a subject'
const PLACEMENT_FAILED = 'The step was added at the end.'

// The canvas grows with the sequence so a short one isn't a tall empty box,
// and stops at a height that still leaves the page around it usable; past
// that, the canvas pans (and follows keyboard focus).
const MIN_CANVAS_HEIGHT = 320
const MAX_CANVAS_HEIGHT = 720
const CANVAS_PADDING = 96

export type SequenceCanvasProps = {
  campaignId: string
  /** Server truth, sorted by `step_order`. */
  steps: StepWithId[]
  canModifyStructure: boolean
  /** Owned by the editor, so its section-bar "Add step" opens this same panel. */
  panel: SequencePanel | null
  onPanelChange: (panel: SequencePanel | null) => void
  onDelete: (step: StepWithId) => void
  onVariants: (target: { step: StepWithId; position: number }) => void
  /** Lifts a reorder failure to the editor's banner; `null` clears it. */
  onReorderError: (message: string | null) => void
}

/**
 * The sequence as a flow: Start → steps → Stop, laid out by dagre. Code-split
 * behind `React.lazy` in `sequence-editor.tsx`, so `@xyflow/react` and dagre
 * only download when the canvas is actually shown.
 *
 * Editing reuses the list's `StepForm` in a side panel rather than a second
 * form. Structural gestures (insert on an edge, move up/down, drag a
 * connection) all reduce to the one reorder endpoint; the create endpoint
 * always appends, so inserting mid-sequence is create-then-place.
 */
export default function SequenceCanvas({
  campaignId,
  steps: serverSteps,
  canModifyStructure,
  panel,
  onPanelChange,
  onDelete,
  onVariants,
  onReorderError,
}: SequenceCanvasProps) {
  const [reorderSteps, reorderState] = useReorderStepsMutation()
  const [focusRequest, setFocusRequest] = useState<string | null>(null)
  const clearFocusRequest = useCallback(() => setFocusRequest(null), [])

  // The order the reorder endpoint answered with, held until the refetch its
  // invalidation triggers replaces `serverSteps`. It can't simply be written
  // into the list cache: while that refetch is in flight, RTK Query's hook
  // keeps returning the previous result's data, so a cache patch is invisible
  // exactly when it matters — and a second move would compute from the
  // pre-move order and silently undo the first. Keyed to the `serverSteps` it
  // was confirmed against, so fresh server data always wins.
  const [confirmed, setConfirmed] = useState<{ base: StepWithId[]; order: string[] } | null>(null)
  const steps = useMemo(
    () => (confirmed?.base === serverSteps ? [...applyOrder(serverSteps, confirmed.order)] : serverSteps),
    [confirmed, serverSteps],
  )
  const latestServerSteps = useRef(serverSteps)
  useEffect(() => {
    latestServerSteps.current = serverSteps
  }, [serverSteps])

  const graph = useMemo(
    () => buildSequenceGraph(steps, { canModifyStructure, outputsOf: sequenceNodeRegistry.outputsOf }),
    [steps, canModifyStructure],
  )
  const nodes = useFlowLayout(graph.nodes, graph.edges, sequenceNodeRegistry)
  const order = useMemo(() => steps.map((step) => step.id), [steps])
  const stepIds = useMemo(() => new Set(order), [order])
  const subjectOf = useMemo(() => {
    const subjects = new Map(steps.map((step) => [step.id, step.subject]))
    return (id: string) => subjects.get(id)
  }, [steps])

  // The order as of the latest server data. A save callback runs after an
  // await, so the order it closed over at submit time can already be stale
  // (a delete or a move landed meanwhile); placement reads this instead.
  const latestOrder = useRef(order)
  useEffect(() => {
    latestOrder.current = order
  }, [order])

  const reorder = useCallback(
    async (next: string[], failurePrefix?: string) => {
      if (!orderChanged(latestOrder.current, next)) return
      onReorderError(null)
      const result = await reorderSteps({ id: campaignId, reorderStepsRequest: { step_ids: next } })
      if ('error' in result) {
        const message = reorderErrorMessage(result.error)
        onReorderError(failurePrefix ? `${failurePrefix} ${message}` : message)
        return
      }
      const confirmedOrder = result.data.flatMap((step) => (step.id ? [step.id] : []))
      setConfirmed({ base: latestServerSteps.current, order: confirmedOrder })
    },
    [campaignId, onReorderError, reorderSteps],
  )

  const moveBlock = useCallback(
    (stepId: string, delta: -1 | 1): MoveBlock | null => {
      const next = moveStep(order, stepId, delta)
      if (!orderChanged(order, next)) return { reason: null }
      if (!leadsWithSubject(next, subjectOf)) return { reason: NEEDS_SUBJECT_FIRST }
      return null
    },
    [order, subjectOf],
  )

  const move = useCallback(
    (stepId: string, delta: -1 | 1) => {
      const next = moveStep(order, stepId, delta)
      // Every move button is disabled while the request is in flight, which
      // drops focus. Hand it back to the same arrow — or, if the step now sits
      // at the end that arrow points past, to the one that still works.
      const index = next.indexOf(stepId)
      const atEnd = delta < 0 ? index === 0 : index === next.length - 1
      const pressed = delta < 0 ? 'up' : 'down'
      const opposite = delta < 0 ? 'down' : 'up'
      setFocusRequest(focusKey(stepId, atEnd ? opposite : pressed))
      void reorder(next)
    },
    [order, reorder],
  )

  const actions = useMemo<SequenceCanvasActions>(
    () => ({
      campaignId,
      editingStepId: panel?.kind === 'edit' ? panel.stepId : null,
      editStep: (stepId) => onPanelChange({ kind: 'edit', stepId, returnFocus: currentFocus() }),
      openVariants: (step, position) => onVariants({ step, position }),
      requestDelete: (step) => {
        // Deleting the step being edited would leave the panel editing nothing.
        if (panel?.kind === 'edit' && panel.stepId === step.id) onPanelChange(null)
        onDelete(step)
      },
      moveStep: move,
      moveBlock,
      isReordering: reorderState.isLoading,
      focusRequest,
      clearFocusRequest,
    }),
    [
      campaignId,
      clearFocusRequest,
      focusRequest,
      move,
      moveBlock,
      onDelete,
      onPanelChange,
      onVariants,
      panel,
      reorderState.isLoading,
    ],
  )

  const onInsert = useCallback(
    (target: FlowInsertTarget) => {
      onPanelChange({
        kind: 'add',
        afterId: target.source === START_NODE_ID ? null : target.source,
        returnFocus: currentFocus(),
      })
    },
    [onPanelChange],
  )

  // A drag is meaningful only if it would also leave a subject on step 1 —
  // the same rule the move buttons and insert-at-start follow.
  const connectionOrder = useCallback(
    (connection: Pick<Edge, 'source' | 'target'>): string[] | null => {
      if (!isMeaningfulConnection(connection, stepIds)) return null
      const next = orderForConnection(order, connection.source, connection.target)
      return leadsWithSubject(next, subjectOf) ? next : null
    },
    [order, stepIds, subjectOf],
  )
  const onConnect = useCallback(
    (connection: Connection) => {
      const next = connectionOrder(connection)
      if (next) void reorder(next)
    },
    [connectionOrder, reorder],
  )
  const isValidConnection = useCallback(
    (connection: Connection | Edge) => connectionOrder(connection) !== null,
    [connectionOrder],
  )

  /** Closes the panel, returning focus to what opened it unless a better target is named. */
  function closePanel(nextFocus?: string) {
    const returnTo = panel?.returnFocus
    onPanelChange(null)
    if (nextFocus) setFocusRequest(nextFocus)
    else if (returnTo?.isConnected) returnTo.focus()
  }

  // A new step lands at the end; move it to where it was asked for. If that
  // move can't happen the step still exists (at the end), and the banner says so.
  function placeNewStep(afterId: string | null, saved: SequenceStep | undefined) {
    if (!saved?.id) {
      closePanel()
      return
    }
    closePanel(focusKey(saved.id, 'edit'))
    const appended = [...latestOrder.current.filter((id) => id !== saved.id), saved.id]
    if (afterId !== null && !appended.includes(afterId)) {
      onReorderError(`${PLACEMENT_FAILED} The step it was meant to follow was removed.`)
      return
    }
    const placed = placeAfter(appended, afterId, saved.id)
    if (!orderChanged(appended, placed)) return
    void reorder(placed, PLACEMENT_FAILED)
  }

  const editing = panel?.kind === 'edit' ? steps.find((step) => step.id === panel.stepId) : undefined
  const editingPosition = editing ? steps.indexOf(editing) + 1 : 0
  const height = Math.min(MAX_CANVAS_HEIGHT, Math.max(MIN_CANVAS_HEIGHT, flowContentHeight(nodes) + CANVAS_PADDING))

  return (
    <SequenceCanvasActionsContext.Provider value={actions}>
      <div className="flex flex-col lg:flex-row">
        <div className="min-w-0 flex-1" style={{ height }}>
          <FlowCanvas
            ariaLabel="Sequence flow"
            nodes={nodes}
            edges={graph.edges}
            registry={sequenceNodeRegistry}
            onInsert={canModifyStructure ? onInsert : undefined}
            onConnect={canModifyStructure ? onConnect : undefined}
            isValidConnection={isValidConnection}
          />
        </div>

        {editing && (
          <SidePanel key={`edit:${editing.id}`} title={`Step ${editingPosition}`} onClose={() => closePanel()}>
            <StepForm
              campaignId={campaignId}
              step={editing}
              isFirstStep={editingPosition === 1}
              onDone={() => closePanel()}
              onCancel={() => closePanel()}
            />
          </SidePanel>
        )}
        {panel?.kind === 'add' && canModifyStructure && (
          <SidePanel
            key={`add:${panel.afterId ?? START_NODE_ID}`}
            title={addTitle(panel.afterId, order)}
            onClose={() => closePanel()}
          >
            <StepForm
              campaignId={campaignId}
              isFirstStep={panel.afterId === null}
              onDone={(saved) => placeNewStep(panel.afterId, saved)}
              onCancel={() => closePanel()}
            />
          </SidePanel>
        )}
      </div>
    </SequenceCanvasActionsContext.Provider>
  )
}

function addTitle(afterId: string | null, order: readonly string[]): string {
  if (afterId === null) return 'New first step'
  const index = order.indexOf(afterId)
  // The anchor was deleted while the panel was open; saving still works (the
  // step lands at the end and the banner says why).
  return index < 0 ? 'New step' : `New step after step ${index + 1}`
}

/**
 * The editor beside the canvas. Focus moves to its heading when it opens, so a
 * keyboard user lands in the form they just asked for. Escape closes it; the
 * canvas then puts focus back on whatever opened it, or on the new step's node
 * after an add.
 */
function SidePanel({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  const headingRef = useRef<HTMLHeadingElement>(null)
  // Mount only (the panel is keyed per target): refocusing on every render
  // would yank focus out of the form mid-typing.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])
  return (
    <aside
      aria-label={title}
      className="border-t border-border bg-surface/60 lg:w-[420px] lg:shrink-0 lg:border-t-0 lg:border-l"
      onKeyDown={(event) => {
        // An Escape something inside already handled — closing the editor's
        // `{{` menu, a link field, a dropdown — must not also discard the form.
        if (event.key === 'Escape' && !event.defaultPrevented) onClose()
      }}
    >
      <h3
        ref={headingRef}
        tabIndex={-1}
        className="border-b border-border px-5 py-2.5 font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint outline-none"
      >
        {title}
      </h3>
      {children}
    </aside>
  )
}
