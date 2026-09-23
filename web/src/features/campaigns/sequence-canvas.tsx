import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Connection, Edge } from '@xyflow/react'
import { FlowCanvas } from '@/components/shared/flow/flow-canvas'
import { flowContentHeight, useFlowLayout } from '@/components/shared/flow/flow-layout'
import type { FlowInsertTarget } from '@/components/shared/flow/flow-insert-context'
import {
  useReorderStepsMutation,
  useSetStepBranchMutation,
  type CampaignGraph,
  type SequenceStep,
  type StepBranch,
} from './api'
import { draftFromBranch, validateDraft, withExit } from './branch-draft'
import { applyBranchWrites, isSuperseded, type BranchWrite } from './branch-overlay'
import { useBranchRules } from './branch-rules'
import { branchErrorMessage } from './branch-error'
import { cycleStepIds } from './branch-loop'
import { ConditionEditor } from './condition-editor'
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
  branchesByStep,
  buildSequenceGraph,
  exitForConnection,
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
  /**
   * The routing. `undefined` together with `graphFailed` means it couldn't be
   * loaded: the flow then draws the steps in order and offers no condition
   * editing, because a save could silently replace a branch nobody can see.
   */
  graph: CampaignGraph | undefined
  graphFailed: boolean
  /** Whether the graph is refetching, and when the request behind `graph` started (see branch-overlay.ts). */
  graphIsFetching: boolean
  graphStartedAt: number | undefined
  /** Refetch the routing — before a condition editor opens, so it starts from the server's latest. */
  onRefreshRouting: () => void
  /** The loop the server last refused, highlighted on its nodes. */
  loopStepIds: readonly string[] | null
  onLoop: (stepIds: string[] | null) => void
  /** Owned by the editor, so its section-bar "Add step" opens this same panel. */
  panel: SequencePanel | null
  onPanelChange: (panel: SequencePanel | null) => void
  onDelete: (step: StepWithId) => void
  onVariants: (target: { step: StepWithId; position: number }) => void
  /** Lifts a failed gesture to the editor's banner; `null` clears it. */
  onNotice: (message: string | null) => void
}

/**
 * The sequence as a flow: Start → steps (and the conditions hanging off them)
 * → Stop, laid out by dagre. Code-split behind `React.lazy` in
 * `sequence-editor.tsx`, so `@xyflow/react` and dagre only download when the
 * canvas is actually shown.
 *
 * Two kinds of edit, gated differently, as the server gates them:
 * - STRUCTURE (insert on an edge, move up/down, drag from Start or a step) all
 *   reduce to the reorder endpoint and are draft-only.
 * - ROUTING (add / edit / remove a condition, drag a condition's exit onto a
 *   step) goes through the branch endpoints and is allowed while running.
 */
export default function SequenceCanvas({
  campaignId,
  steps: serverSteps,
  canModifyStructure,
  graph: routing,
  graphFailed,
  graphIsFetching,
  graphStartedAt,
  onRefreshRouting,
  loopStepIds,
  onLoop,
  panel,
  onPanelChange,
  onDelete,
  onVariants,
  onNotice,
}: SequenceCanvasProps) {
  const [reorderSteps, reorderState] = useReorderStepsMutation()
  const [setBranch, setBranchState] = useSetStepBranchMutation()
  const rules = useBranchRules(campaignId)
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

  // A failed refetch keeps the last graph it had (RTK leaves `data` in place),
  // so the flow still draws what is known — but nothing may be written against
  // it until the routing loads again.
  const canEditBranches = !graphFailed && routing !== undefined
  const [writes, setWrites] = useState<BranchWrite[]>([])
  const freshness = useMemo(
    () => ({ isFetching: graphIsFetching, startedAt: graphStartedAt }),
    [graphIsFetching, graphStartedAt],
  )
  const branches = useMemo(
    () => applyBranchWrites(branchesByStep(routing?.nodes), writes, freshness),
    [freshness, routing, writes],
  )
  const recordWrite = useCallback(
    (stepId: string, branch: StepBranch | null) => {
      // Date.now() once the response is in: the server committed before it
      // answered, so any graph request started after this moment includes it.
      const write = { stepId, branch, savedAt: Date.now() }
      setWrites((current) => [...current.filter((entry) => !isSuperseded(entry, freshness)), write])
    },
    [freshness],
  )
  const loop = useMemo(() => new Set(loopStepIds ?? []), [loopStepIds])

  const flow = useMemo(
    () =>
      buildSequenceGraph(steps, {
        canModifyStructure,
        canEditBranches,
        branches,
        loopStepIds: loop,
        outputsOf: sequenceNodeRegistry.outputsOf,
      }),
    [steps, canModifyStructure, canEditBranches, branches, loop],
  )
  const { nodes, edges } = useFlowLayout(flow.nodes, flow.edges, sequenceNodeRegistry)
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
      onNotice(null)
      onLoop(null)
      const result = await reorderSteps({ id: campaignId, reorderStepsRequest: { step_ids: next } })
      if ('error' in result) {
        const message = reorderErrorMessage(result.error)
        onNotice(failurePrefix ? `${failurePrefix} ${message}` : message)
        // With conditions in play a new order can close a loop through the
        // fall-through; the server names it, so point at it.
        onLoop(cycleStepIds(result.error))
        return
      }
      const confirmedOrder = result.data.flatMap((step) => (step.id ? [step.id] : []))
      setConfirmed({ base: latestServerSteps.current, order: confirmedOrder })
    },
    [campaignId, onLoop, onNotice, reorderSteps],
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
      editingConditionStepId: panel?.kind === 'condition' ? panel.stepId : null,
      editStep: (stepId) => onPanelChange({ kind: 'edit', stepId, returnFocus: currentFocus() }),
      editCondition: (stepId) => {
        onRefreshRouting()
        onPanelChange({ kind: 'condition', stepId, returnFocus: currentFocus() })
      },
      openVariants: (step, position) => onVariants({ step, position }),
      requestDelete: (step) => {
        // Deleting the step a panel is about would leave it editing nothing.
        if (panel && panel.kind !== 'add' && panel.stepId === step.id) onPanelChange(null)
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
      onRefreshRouting,
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

  // What a drag means depends on where it starts. From a condition's exit it
  // sets that exit (routing, allowed while running). From Start or a step it
  // is a reorder (structure, draft-only) — and only from a step whose next is
  // still the fall-through, since a condition owns the rest; the reorder must
  // also leave a subject on step 1, the rule the move buttons follow.
  //
  // An exit drag is refused while another branch write is in flight: it would
  // be built from the branch as it stood before that write answered.
  const exitFor = useCallback(
    (connection: Pick<Edge, 'source' | 'target' | 'sourceHandle'>) => {
      if (!canEditBranches || setBranchState.isLoading) return null
      const exit = exitForConnection(connection, branches, stepIds)
      const branch = exit ? branches.get(exit.stepId) : undefined
      if (!exit || !branch) return null
      // Dropping an exit where it already goes changes nothing: don't write.
      const current = exit.exit === 'yes' ? branch.yes_step_id : branch.no_step_id
      return current === exit.target ? null : { ...exit, branch }
    },
    [branches, canEditBranches, setBranchState.isLoading, stepIds],
  )
  const reorderFor = useCallback(
    (connection: Pick<Edge, 'source' | 'target'>): string[] | null => {
      // With the routing unknown, a reorder could re-link a fall-through into
      // a branch nobody can see — hold structure drags until it loads.
      if (!canModifyStructure || graphFailed || branches.has(connection.source)) return null
      if (!isMeaningfulConnection(connection, stepIds)) return null
      const next = orderForConnection(order, connection.source, connection.target)
      return leadsWithSubject(next, subjectOf) ? next : null
    },
    [branches, canModifyStructure, graphFailed, order, stepIds, subjectOf],
  )

  const setExit = useCallback(
    async (connection: Connection) => {
      const exit = exitFor(connection)
      if (!exit) return
      const request = withExit(exit.branch, exit.exit, exit.target)
      // Checked against the same rules the editor applies. A drag that breaks
      // one (tracking turned off since the condition was saved, a label that
      // now stops the sequence) opens the editor on the dragged draft with the
      // problem shown by its field, instead of a banner about a server refusal.
      const draft = draftFromBranch(
        exit.exit === 'yes' ? { ...exit.branch, yes_step_id: exit.target } : { ...exit.branch, no_step_id: exit.target },
      )
      const problems = validateDraft(draft, {
        stepId: exit.stepId,
        stepIds,
        trackingEnabled: rules.trackingEnabled,
        htmlEverywhere: undefined,
        replyLabels: rules.replyLabels,
      })
      if (problems.length > 0) {
        onPanelChange({ kind: 'condition', stepId: exit.stepId, returnFocus: currentFocus(), draft })
        return
      }
      onNotice(null)
      onLoop(null)
      const result = await setBranch({ id: campaignId, stepId: exit.stepId, stepBranchRequest: request })
      if ('error' in result) {
        onNotice(branchErrorMessage(result.error))
        onLoop(cycleStepIds(result.error))
        return
      }
      recordWrite(exit.stepId, result.data)
    },
    [campaignId, exitFor, onLoop, onNotice, onPanelChange, recordWrite, rules, setBranch, stepIds],
  )
  const onConnect = useCallback(
    (connection: Connection) => {
      if (exitFor(connection)) {
        void setExit(connection)
        return
      }
      const next = reorderFor(connection)
      if (next) void reorder(next)
    },
    [exitFor, reorder, reorderFor, setExit],
  )
  const isValidConnection = useCallback(
    (connection: Connection | Edge) => exitFor(connection) !== null || reorderFor(connection) !== null,
    [exitFor, reorderFor],
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
      onNotice(`${PLACEMENT_FAILED} The step it was meant to follow was removed.`)
      return
    }
    const placed = placeAfter(appended, afterId, saved.id)
    if (!orderChanged(appended, placed)) return
    void reorder(placed, PLACEMENT_FAILED)
  }

  const panelStep = panel && panel.kind !== 'add' ? steps.find((step) => step.id === panel.stepId) : undefined
  const panelPosition = panelStep ? steps.indexOf(panelStep) + 1 : 0
  const height = Math.min(MAX_CANVAS_HEIGHT, Math.max(MIN_CANVAS_HEIGHT, flowContentHeight(nodes) + CANVAS_PADDING))

  return (
    <SequenceCanvasActionsContext.Provider value={actions}>
      <div className="flex flex-col lg:flex-row">
        <div className="min-w-0 flex-1" style={{ height }}>
          <FlowCanvas
            ariaLabel="Sequence flow"
            nodes={nodes}
            edges={edges}
            registry={sequenceNodeRegistry}
            onInsert={canModifyStructure ? onInsert : undefined}
            onConnect={onConnect}
            isValidConnection={isValidConnection}
          />
        </div>

        {panel?.kind === 'edit' && panelStep && (
          <SidePanel key={`edit:${panelStep.id}`} title={`Step ${panelPosition}`} onClose={() => closePanel()}>
            <StepForm
              campaignId={campaignId}
              step={panelStep}
              isFirstStep={panelPosition === 1}
              onDone={() => closePanel()}
              onCancel={() => closePanel()}
            />
          </SidePanel>
        )}
        {/* Stays mounted if a refetch fails while it's open — with saving
            held — rather than vanishing with the user's draft in it. */}
        {panel?.kind === 'condition' && panelStep && routing !== undefined && (
          <SidePanel
            key={`condition:${panelStep.id}`}
            title={`Condition after step ${panelPosition}`}
            onClose={() => closePanel()}
          >
            <ConditionEditor
              campaignId={campaignId}
              step={panelStep}
              position={panelPosition}
              steps={steps}
              branch={branches.get(panelStep.id) ?? null}
              initialDraft={panel.draft}
              routingUnavailable={graphFailed}
              onWritten={(branch) => recordWrite(panelStep.id, branch)}
              onSaved={() => closePanel(focusKey(panelStep.id, 'condition'))}
              onDeleted={() => closePanel(focusKey(panelStep.id, 'add-condition'))}
              onCancel={() => closePanel()}
              onLoop={onLoop}
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
 * canvas then puts focus back on whatever opened it, or on what the save
 * created (the new step, the new condition).
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
