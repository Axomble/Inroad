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
import type { ReplyLabel } from './api'
import { useReorderReplyLabelsMutation } from './api'
import { LabelRowBody } from './label-row'
import { replyLabelErrorMessage } from './error-messages'

/**
 * Drag-reorder list for reply labels. Isolated into its own module so
 * `@dnd-kit/*` (~50kB raw) stays code-split behind `React.lazy` — the Suspense
 * fallback renders the same rows statically from the main chunk with no dnd
 * dependency (same rationale as campaigns/sortable-step-list.tsx). Owns the
 * optimistic local order + revert-on-error; reorder errors are lifted to the
 * parent banner via `onReorderError`.
 */
type SortableLabelListProps = {
  /** Server-truth labels, already sorted by `position`. */
  labels: ReplyLabel[]
  onEdit: (label: ReplyLabel) => void
  onDelete: (label: ReplyLabel) => void
  onReorderError: (message: string | null) => void
  /** Reconcile with server truth after a reverted optimistic reorder. */
  refetch: () => void
}

export default function SortableLabelList({ labels, onEdit, onDelete, onReorderError, refetch }: SortableLabelListProps) {
  const [reorderLabels] = useReorderReplyLabelsMutation()

  // Local order (list of ids) drives rendering so reorder can be optimistic.
  // Resync from server truth whenever the fetched labels change (initial load,
  // a committed reorder, or a revert-refetch).
  const [order, setOrder] = useState<string[]>([])
  useEffect(() => {
    setOrder(labels.map((l) => l.id))
  }, [labels])

  const byId = useMemo(() => new Map(labels.map((l) => [l.id, l])), [labels])
  const orderedLabels = order.map((id) => byId.get(id)).filter((l): l is ReplyLabel => l !== undefined)

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  async function onDragEnd(event: DragEndEvent) {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const oldIndex = order.indexOf(String(active.id))
    const newIndex = order.indexOf(String(over.id))
    if (oldIndex < 0 || newIndex < 0) return

    const previous = order
    const next = arrayMove(order, oldIndex, newIndex)
    setOrder(next) // optimistic
    onReorderError(null)

    // The server demands every label exactly once — `next` is a permutation of
    // the fetched set, so a 422 here means the workspace changed underneath us.
    const result = await reorderLabels({ replyLabelReorderInput: { ids: next } })
    if ('error' in result) {
      setOrder(previous) // revert optimistic order
      refetch() // reconcile with server truth
      onReorderError(replyLabelErrorMessage('reorder', result.error))
    }
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={order} strategy={verticalListSortingStrategy}>
        <div>
          {orderedLabels.map((label) => (
            <SortableLabelRow
              key={label.id}
              label={label}
              onEdit={() => onEdit(label)}
              onDelete={() => onDelete(label)}
            />
          ))}
        </div>
      </SortableContext>
    </DndContext>
  )
}

function SortableLabelRow({
  label,
  onEdit,
  onDelete,
}: {
  label: ReplyLabel
  onEdit: () => void
  onDelete: () => void
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: label.id })
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : undefined,
  }

  return (
    <div ref={setNodeRef} style={style} className={isDragging ? 'relative z-10' : undefined}>
      <LabelRowBody
        label={label}
        onEdit={onEdit}
        onDelete={onDelete}
        dragHandle={
          <button
            type="button"
            aria-label={`Reorder label ${label.label}`}
            className="flex size-6 shrink-0 cursor-grab touch-none items-center justify-center rounded text-faint outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing"
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
