import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Connection, Edge } from '@xyflow/react'
import { FlowCanvas } from '@/components/shared/flow/flow-canvas'
import { useFlowLayout } from '@/components/shared/flow/flow-layout'
import type { FlowInsertTarget } from '@/components/shared/flow/flow-insert-context'
import { useReorderStepsMutation, type SequenceStep } from './api'
import {
  SequenceCanvasActionsContext,
  type SequenceCanvasActions,
} from './sequence-canvas-actions'
import {
  START_NODE_ID,
  buildSequenceGraph,
  isMeaningfulConnection,
  moveStep,
  orderChanged,
  orderForConnection,
  placeAfter,
} from './sequence-graph'
import { sequenceNodeRegistry } from './sequence-node-registry'
import type { StepWithId } from './step-card'
import { reorderErrorMessage } from './step-error'
import { StepForm } from './step-form'

/** What the side panel is showing: one step's editor, or a new step's, anchored after a node. */
type Panel = { kind: 'edit'; stepId: string } | { kind: 'add'; afterId: string | null }

export type SequenceCanvasProps = {
  campaignId: string
  /** Server truth, sorted by `step_order`. */
  steps: StepWithId[]
  canModifyStructure: boolean
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
  steps,
  canModifyStructure,
  onDelete,
  onVariants,
  onReorderError,
}: SequenceCanvasProps) {
  const [panel, setPanel] = useState<Panel | null>(null)
  const [reorderSteps, reorderState] = useReorderStepsMutation()

  const graph = useMemo(
    () => buildSequenceGraph(steps, { canModifyStructure, outputsOf: sequenceNodeRegistry.outputsOf }),
    [steps, canModifyStructure],
  )
  const nodes = useFlowLayout(graph.nodes, graph.edges, sequenceNodeRegistry)
  const order = useMemo(() => steps.map((step) => step.id), [steps])
  const stepIds = useMemo(() => new Set(order), [order])

  const reorder = useCallback(
    async (next: string[], failurePrefix?: string) => {
      if (!orderChanged(order, next)) return
      onReorderError(null)
      const result = await reorderSteps({ id: campaignId, reorderStepsRequest: { step_ids: next } })
      if ('error' in result) {
        const message = reorderErrorMessage(result.error)
        onReorderError(failurePrefix ? `${failurePrefix} ${message}` : message)
      }
    },
    [campaignId, onReorderError, order, reorderSteps],
  )

  const actions = useMemo<SequenceCanvasActions>(
    () => ({
      campaignId,
      editingStepId: panel?.kind === 'edit' ? panel.stepId : null,
      editStep: (stepId) => setPanel({ kind: 'edit', stepId }),
      openVariants: (step, position) => onVariants({ step, position }),
      requestDelete: (step) => {
        // Deleting the step being edited would leave the panel editing nothing.
        setPanel((current) => (current?.kind === 'edit' && current.stepId === step.id ? null : current))
        onDelete(step)
      },
      moveStep: (stepId, delta) => void reorder(moveStep(order, stepId, delta)),
      isReordering: reorderState.isLoading,
    }),
    [campaignId, onDelete, onVariants, order, panel, reorder, reorderState.isLoading],
  )

  const onInsert = useCallback((target: FlowInsertTarget) => {
    setPanel({ kind: 'add', afterId: target.source === START_NODE_ID ? null : target.source })
  }, [])

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!isMeaningfulConnection(connection, stepIds)) return
      void reorder(orderForConnection(order, connection.source, connection.target))
    },
    [order, reorder, stepIds],
  )
  const isValidConnection = useCallback(
    (connection: Connection | Edge) => isMeaningfulConnection(connection, stepIds),
    [stepIds],
  )

  const closePanel = () => setPanel(null)

  // A new step lands at the end; move it to where it was asked for. If that
  // move fails the step still exists (at the end), and the banner says so.
  function placeNewStep(afterId: string | null, saved: SequenceStep | undefined) {
    closePanel()
    if (!saved?.id) return
    const appended = [...order, saved.id]
    const placed = placeAfter(appended, afterId, saved.id)
    if (!orderChanged(appended, placed)) return
    void reorder(placed, 'The step was added at the end.')
  }

  const editing = panel?.kind === 'edit' ? steps.find((step) => step.id === panel.stepId) : undefined
  const editingPosition = editing ? steps.indexOf(editing) + 1 : 0

  return (
    <SequenceCanvasActionsContext.Provider value={actions}>
      <div className="flex flex-col lg:flex-row">
        <div className="h-[560px] min-w-0 flex-1">
          <FlowCanvas
            ariaLabel="Sequence flow"
            nodes={nodes}
            edges={graph.edges}
            nodeTypes={sequenceNodeRegistry.nodeTypes}
            onInsert={canModifyStructure ? onInsert : undefined}
            onConnect={canModifyStructure ? onConnect : undefined}
            isValidConnection={isValidConnection}
          />
        </div>

        {editing && (
          <SidePanel key={`edit:${editing.id}`} title={`Step ${editingPosition}`} onClose={closePanel}>
            <StepForm
              campaignId={campaignId}
              step={editing}
              isFirstStep={editingPosition === 1}
              onDone={closePanel}
              onCancel={closePanel}
            />
          </SidePanel>
        )}
        {panel?.kind === 'add' && canModifyStructure && (
          <SidePanel key={`add:${panel.afterId ?? START_NODE_ID}`} title={addTitle(panel.afterId, order)} onClose={closePanel}>
            <StepForm
              campaignId={campaignId}
              isFirstStep={panel.afterId === null}
              onDone={(saved) => placeNewStep(panel.afterId, saved)}
              onCancel={closePanel}
            />
          </SidePanel>
        )}
      </div>
    </SequenceCanvasActionsContext.Provider>
  )
}

function addTitle(afterId: string | null, order: readonly string[]): string {
  if (afterId === null) return 'New first step'
  return `New step after step ${order.indexOf(afterId) + 1}`
}

/**
 * The editor beside the canvas. Takes focus when it opens so a keyboard user
 * lands in the form they just asked for, and Escape hands them back.
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
        if (event.key === 'Escape') onClose()
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
