import { useEffect, useMemo, useState } from 'react'
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { GripVertical } from 'lucide-react'
import { httpStatus } from '@/lib/rtk-error'
import { useReorderStepsMutation } from './api'
import { StepCardBody, type StepWithId } from './step-card'
import { StepForm } from './step-form'

/**
 * Draft-only drag-reorder list. Isolated into its own module so `@dnd-kit/*`
 * (~50kB raw) is code-split behind `React.lazy` and only downloaded when a
 * draft campaign's reorder UI mounts — the non-draft path and the Suspense
 * fallback render the static list from the main campaigns chunk with no dnd
 * dependency. Owns the optimistic local order + revert-on-error so the parent
 * stays free of dnd concerns; reorder errors are lifted to the parent banner
 * via `onReorderError`.
 */
type SortableStepListProps = {
  campaignId: string
  /** Server-truth steps, already sorted by `step_order`. */
  steps: StepWithId[]
  editingId: string | null
  onEdit: (id: string) => void
  onEditDone: () => void
  onDelete: (step: StepWithId) => void
  onVariants: (target: { step: StepWithId; position: number }) => void
  onReorderError: (message: string | null) => void
  /** Reconcile with server truth after a reverted optimistic reorder. */
  refetch: () => void
}

export default function SortableStepList({
  campaignId,
  steps,
  editingId,
  onEdit,
  onEditDone,
  onDelete,
  onVariants,
  onReorderError,
  refetch,
}: SortableStepListProps) {
  const [reorderSteps] = useReorderStepsMutation()

  // Local order (list of ids) drives rendering so reorder can be optimistic.
  // Resync from server truth whenever the fetched steps change (initial load,
  // a committed reorder, or a revert-refetch). During the optimistic window the
  // query data is unchanged, so this effect does not clobber it.
  const [order, setOrder] = useState<string[]>([])
  useEffect(() => {
    setOrder(steps.map((s) => s.id))
  }, [steps])

  const byId = useMemo(() => new Map(steps.map((s) => [s.id, s])), [steps])
  const orderedSteps = order.map((id) => byId.get(id)).filter((s): s is StepWithId => s !== undefined)

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  async function onDragEnd(event: DragEndEvent) {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const activeId = String(active.id)
    const overId = String(over.id)
    const oldIndex = order.indexOf(activeId)
    const newIndex = order.indexOf(overId)
    if (oldIndex < 0 || newIndex < 0) return

    const previous = order
    const next = arrayMove(order, oldIndex, newIndex)
    setOrder(next) // optimistic
    onReorderError(null)

    const result = await reorderSteps({ id: campaignId, reorderStepsRequest: { step_ids: next } })
    if ('error' in result) {
      setOrder(previous) // revert optimistic order
      refetch() // reconcile with server truth
      const st = httpStatus(result.error)
      onReorderError(
        st === 409 ? 'Reorder is only allowed while the campaign is a draft.' : "Couldn't reorder steps — try again.",
      )
    }
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={order} strategy={verticalListSortingStrategy}>
        <ul>
          {orderedSteps.map((step, i) =>
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
              <SortableStepCard
                key={step.id}
                step={step}
                position={i + 1}
                threadSubject={orderedSteps[0]?.subject}
                onEdit={() => onEdit(step.id)}
                onDelete={() => onDelete(step)}
                onVariants={() => onVariants({ step, position: i + 1 })}
              />
            ),
          )}
        </ul>
      </SortableContext>
    </DndContext>
  )
}

/**
 * Draft card: draggable + reorderable. Wires `useSortable` and renders a
 * keyboard-accessible drag handle (dnd-kit's sortable attributes/listeners make
 * the handle operable with pointer and keyboard).
 */
function SortableStepCard({
  step,
  position,
  threadSubject,
  onEdit,
  onDelete,
  onVariants,
}: {
  step: StepWithId
  position: number
  threadSubject?: string
  onEdit: () => void
  onDelete: () => void
  onVariants: () => void
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: step.id })
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : undefined,
  }

  return (
    <div ref={setNodeRef} style={style} className={isDragging ? 'relative z-10' : undefined}>
      <StepCardBody
        step={step}
        position={position}
        threadSubject={threadSubject}
        canModifyStructure
        onEdit={onEdit}
        onDelete={onDelete}
        onVariants={onVariants}
        dragHandle={
          <button
            type="button"
            aria-label={`Reorder step ${position}`}
            className="mt-0.5 flex size-6 shrink-0 cursor-grab touch-none items-center justify-center rounded text-faint outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing"
            {...attributes}
            {...listeners}
          >
            <GripVertical className="size-4" />
          </button>
        }
      />
    </div>
  )
}
