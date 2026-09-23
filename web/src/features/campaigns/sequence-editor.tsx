import { Suspense, lazy, useMemo, useState } from 'react'
import { List, Plus, Workflow } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { SectionBar, EmptyBlock } from '@/components/layout/page'
import { NoticeBanner } from '@/components/shared/notice-banner'
import { QueryErrorBanner } from '@/components/shared/record-page'
import { httpStatus } from '@/lib/rtk-error'
import { useListStepsQuery, useDeleteStepMutation, useGetCampaignGraphQuery, type SequenceStep } from './api'
import { cycleStepIds } from './branch-loop'
import { StepCard, type StepWithId } from './step-card'
import { VariantsDialog } from './variants-dialog'
import { StepForm } from './step-form'
import { stepErrorMessage } from './step-error'
// Type-only, and from the small actions module rather than the lazy canvas, so
// the eager chunk stays free of React Flow.
import { currentFocus, type SequencePanel } from './sequence-canvas-actions'

// Drag-reorder only mounts for DRAFT campaigns, so keep `@dnd-kit` out of the
// eager campaigns chunk: the sortable list is code-split and pulled in behind a
// Suspense boundary (fallback = the static list) only when a draft is opened.
const SortableStepList = lazy(() => import('./sortable-step-list'))

// The flow canvas is the default view, but `@xyflow/react` + dagre are the
// heaviest thing on this screen and nothing else in the app needs them — so
// they stay in their own chunk, fetched when the canvas first mounts.
const SequenceCanvas = lazy(() => import('./sequence-canvas'))

type SequenceView = 'canvas' | 'list'

const DRAFT_ONLY_HINT = 'Structural changes are draft-only'

/** Narrows a step to one with a defined id (every persisted step has one). */
function hasId(step: SequenceStep): step is StepWithId {
  return typeof step.id === 'string' && step.id.length > 0
}

/**
 * The campaign's sequence editor, shown as a flow canvas (default) or an ordered
 * list of step cards, both with add / edit / delete / reorder. The editor owns
 * what both views share — the query, the delete confirmation, the variants
 * dialog, the reorder banner, the canvas's side panel — so switching views
 * loses none of it. Structural edits (add, delete, reorder) are draft-only;
 * content edits and conditions are available in any status. Owns its own
 * loading / empty / error states so the parent mounts it unconditionally.
 */
export function SequenceEditor({ campaignId, status }: { campaignId: string; status: string | undefined }) {
  const isDraft = status === 'draft'
  const { data, isLoading, error, refetch } = useListStepsQuery({ id: campaignId })
  // The routing (conditions). Its own query: the step list stays the source of
  // content, and a failure here degrades the flow to the linear view instead
  // of hiding the sequence.
  //
  // `refetchOnFocus`: another tab's condition edits arrive when this one is
  // looked at again, not on the next manual reload. It narrows cross-tab
  // last-writer-wins; it doesn't close it (see branch-overlay.ts).
  const graphQuery = useGetCampaignGraphQuery({ id: campaignId }, { refetchOnFocus: true })
  const hasBranches = graphQuery.data?.nodes.some((node) => node.branch !== null) ?? false
  // The step whose A/B variants are open, plus its 1-based position for the
  // dialog title. Held here rather than in the card so only one dialog can be
  // mounted at a time.
  const [variantsFor, setVariantsFor] = useState<{ step: StepWithId; position: number } | null>(null)
  const [deleteStep, deleteState] = useDeleteStepMutation()

  const [adding, setAdding] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [pendingDelete, setPendingDelete] = useState<StepWithId | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // The loop the server last refused (a branch write, a reorder, a delete),
  // marked on the flow's nodes until the next attempt.
  // Held with the step list and graph it was reported against, and shown only
  // while those are still what's on screen: once either changes, the marks
  // may point at a loop that no longer exists.
  const [loopReport, setLoopReport] = useState<{ ids: string[]; steps: unknown; graph: unknown } | null>(null)
  const loop =
    loopReport && loopReport.steps === data && loopReport.graph === graphQuery.data ? loopReport.ids : null
  const setLoop = (ids: string[] | null) =>
    setLoopReport(ids ? { ids, steps: data, graph: graphQuery.data } : null)
  const [view, setView] = useState<SequenceView>('canvas')
  const [canvasPanel, setCanvasPanel] = useState<SequencePanel | null>(null)

  // Server truth, sorted by step_order and narrowed to steps with ids.
  const serverSteps = useMemo(() => {
    const steps = (data ?? []).filter(hasId)
    return [...steps].sort((a, b) => (a.step_order ?? 0) - (b.step_order ?? 0))
  }, [data])

  const stopEditing = () => setEditingId(null)
  const canvasShown = view === 'canvas' && serverSteps.length > 0

  // One add form at a time: on the canvas, "Add step" opens the canvas's own
  // panel (after the last step), never the list's inline form beside it.
  function startAdding() {
    const last = serverSteps.at(-1)
    if (canvasShown && last) {
      setCanvasPanel({ kind: 'add', afterId: last.id, returnFocus: currentFocus() })
      return
    }
    setAdding((open) => !open)
  }

  function switchView(next: SequenceView) {
    setView(next)
    setAdding(false)
    setCanvasPanel(null)
    setEditingId(null)
  }

  // Clear any stale mutation error before opening a fresh confirm dialog so the
  // banner reflects only this delete attempt.
  function requestDelete(step: StepWithId) {
    deleteState.reset()
    setPendingDelete(step)
  }

  async function confirmDelete() {
    if (!pendingDelete) return
    setLoop(null)
    const result = await deleteStep({ id: campaignId, stepId: pendingDelete.id })
    // Close only on success; on error keep the dialog OPEN so the rendered
    // delete error is visible and the user can retry. A delete re-links the
    // fall-through, which can close a loop through a condition — mark it.
    if ('error' in result) setLoop(cycleStepIds(result.error))
    else setPendingDelete(null)
  }

  return (
    <div className="border-b border-border bg-surface/40">
      <SectionBar label="Sequence" count={serverSteps.length || undefined}>
        <ViewToggle view={view} onChange={switchView} />
        {isDraft ? (
          <Button variant="secondary" size="xs" onClick={startAdding}>
            <Plus className="size-3.5" />
            Add step
          </Button>
        ) : (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex">
                  <Button
                    variant="secondary"
                    size="xs"
                    disabled
                    aria-label={`Add step (disabled — ${DRAFT_ONLY_HINT})`}
                  >
                    <Plus className="size-3.5" />
                    Add step
                  </Button>
                </span>
              </TooltipTrigger>
              <TooltipContent>{DRAFT_ONLY_HINT}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </SectionBar>

      {notice && <NoticeBanner notice={{ tone: 'error', text: notice }} />}
      {serverSteps.length > 0 && graphQuery.isError && (
        <QueryErrorBanner
          className="mx-5 my-3"
          message="Couldn’t load this sequence’s conditions, so where each path goes may not be shown, and conditions — and dragging steps — are held until they load."
          onRetry={() => void graphQuery.refetch()}
          retrying={graphQuery.isFetching}
        />
      )}
      {view === 'list' && hasBranches && (
        <p role="note" className="border-b border-border px-5 py-2 text-xs text-muted-foreground">
          This sequence has conditions. The list shows steps in order only — switch to Flow to see where each path goes.
        </p>
      )}

      {isDraft && adding && (
        <StepForm
          campaignId={campaignId}
          isFirstStep={serverSteps.length === 0}
          onDone={() => setAdding(false)}
          onCancel={() => setAdding(false)}
        />
      )}

      {isLoading ? (
        <LoadingRows />
      ) : error ? (
        <div role="alert" className="px-5 py-6 text-sm text-danger">
          Couldn't load the sequence{httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.
        </div>
      ) : serverSteps.length === 0 ? (
        <EmptyBlock
          title="No steps yet"
          description={
            isDraft
              ? 'Add the first step to define what this campaign sends. A step is a subject, a body, and a delay after the previous send.'
              : 'This campaign has no sequence steps.'
          }
        />
      ) : view === 'canvas' ? (
        // Wait for the routing too, so a branched sequence never flashes its
        // linear fall-back first.
        graphQuery.isLoading ? (
          <CanvasLoading />
        ) : (
          <Suspense fallback={<CanvasLoading />}>
            <SequenceCanvas
              campaignId={campaignId}
              steps={serverSteps}
              canModifyStructure={isDraft}
              graph={graphQuery.data}
              graphFailed={graphQuery.isError}
              graphIsFetching={graphQuery.isFetching}
              graphStartedAt={graphQuery.startedTimeStamp}
              onRefreshRouting={() => void graphQuery.refetch()}
              loopStepIds={loop}
              onLoop={setLoop}
              panel={canvasPanel}
              onPanelChange={setCanvasPanel}
              onDelete={requestDelete}
              onVariants={setVariantsFor}
              onNotice={setNotice}
            />
          </Suspense>
        )
      ) : isDraft ? (
        <Suspense
          fallback={
            <StaticStepList
              campaignId={campaignId}
              steps={serverSteps}
              editingId={editingId}
              onEdit={setEditingId}
              onEditDone={stopEditing}
              onDelete={requestDelete}
              onVariants={setVariantsFor}
            />
          }
        >
          <SortableStepList
            campaignId={campaignId}
            steps={serverSteps}
            editingId={editingId}
            onEdit={setEditingId}
            onEditDone={stopEditing}
            onDelete={requestDelete}
            onVariants={setVariantsFor}
            onReorderError={setNotice}
            refetch={refetch}
          />
        </Suspense>
      ) : (
        <StaticStepList
          campaignId={campaignId}
          steps={serverSteps}
          editingId={editingId}
          onEdit={setEditingId}
          onEditDone={stopEditing}
          onDelete={requestDelete}
          onVariants={setVariantsFor}
        />
      )}

      {variantsFor && (
        <VariantsDialog
          campaignID={campaignId}
          step={variantsFor.step}
          position={variantsFor.position}
          onClose={() => setVariantsFor(null)}
        />
      )}

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this step?</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete?.subject ? `“${pendingDelete.subject}” ` : 'This step '}
              will be removed from the sequence and the remaining steps renumbered. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {deleteState.error && (
            <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
              {stepErrorMessage(deleteState.error)}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteState.isLoading}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-destructive-foreground hover:bg-danger/90"
              disabled={deleteState.isLoading}
              onClick={(e) => {
                e.preventDefault()
                void confirmDelete()
              }}
            >
              Delete step
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

/**
 * Static (non-draft) step list, also used as the Suspense fallback while the
 * draft-only sortable list loads. Renders plain `StepCard`s (no `@dnd-kit`) plus
 * the inline edit form, so it lives in the eager campaigns chunk with no layout
 * jank between fallback and the loaded sortable list.
 */
function StaticStepList({
  campaignId,
  steps,
  editingId,
  onEdit,
  onEditDone,
  onDelete,
  onVariants,
}: {
  campaignId: string
  steps: StepWithId[]
  editingId: string | null
  onEdit: (id: string) => void
  onEditDone: () => void
  onDelete: (step: StepWithId) => void
  onVariants: (target: { step: StepWithId; position: number }) => void
}) {
  return (
    <ul>
      {steps.map((step, i) =>
        editingId === step.id ? (
          <li key={step.id} className="border-b border-border last:border-b-0">
            <StepForm
              campaignId={campaignId}
              step={step}
              isFirstStep={i === 0}
              onDone={onEditDone}
              onCancel={onEditDone}
            />
          </li>
        ) : (
          <StepCard
            key={step.id}
            step={step}
            position={i + 1}
            threadSubject={steps[0]?.subject}
            onEdit={() => onEdit(step.id)}
            onDelete={() => onDelete(step)}
            onVariants={() => onVariants({ step, position: i + 1 })}
          />
        ),
      )}
    </ul>
  )
}

/**
 * Flow / list switch. Both views edit the same steps through the same
 * mutations; the list stays for dense scanning and for the drag handle.
 */
function ViewToggle({ view, onChange }: { view: SequenceView; onChange: (view: SequenceView) => void }) {
  return (
    <div role="group" aria-label="Sequence view" className="flex items-center gap-1">
      <Button
        variant={view === 'canvas' ? 'secondary' : 'ghost'}
        size="xs"
        aria-pressed={view === 'canvas'}
        onClick={() => onChange('canvas')}
      >
        <Workflow className="size-3.5" />
        Flow
      </Button>
      <Button
        variant={view === 'list' ? 'secondary' : 'ghost'}
        size="xs"
        aria-pressed={view === 'list'}
        onClick={() => onChange('list')}
      >
        <List className="size-3.5" />
        List
      </Button>
    </div>
  )
}

/** Holds the canvas's footprint while its chunk loads, so the page doesn't jump. */
function CanvasLoading() {
  return (
    <div role="status" aria-label="Loading the sequence flow" className="h-[560px] p-5">
      <Skeleton className="h-full w-full rounded-lg" />
    </div>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1].map((i) => (
        <li key={i} className="flex items-center gap-3 border-b border-border px-5 py-3.5 last:border-b-0">
          <Skeleton className="size-6 rounded-md" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3 w-40" />
            <Skeleton className="h-3.5 w-64" />
          </div>
        </li>
      ))}
    </ul>
  )
}
