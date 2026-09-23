import { BaseEdge, EdgeLabelRenderer, getSmoothStepPath, type Edge, type EdgeProps } from '@xyflow/react'
import { AddNodeButton } from './add-node-button'
import { useFlowInsert } from './flow-insert-context'

export type FlowEdgeData = {
  /** A chip on the edge — what happens between the two nodes ("Wait 3 days"). */
  label?: string
  /** Present = the edge offers an add button; the text is that button's accessible name. */
  insertLabel?: string
}

export type FlowEdgeType = Edge<FlowEdgeData, 'flow'>

/**
 * The canvas's one edge: a rounded orthogonal path with an optional chip and an
 * optional add button at its midpoint. Both are real DOM (not SVG text) so they
 * take focus, read to a screen reader, and follow the theme tokens.
 */
export function FlowEdge({
  id,
  source,
  target,
  sourceHandleId,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  data,
}: EdgeProps<FlowEdgeType>) {
  const onInsert = useFlowInsert()
  const [path, labelX, labelY] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    borderRadius: 10,
  })
  const insertLabel = data?.insertLabel
  const canInsert = insertLabel !== undefined && onInsert !== null

  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} />
      {(data?.label || canInsert) && (
        <EdgeLabelRenderer>
          <div
            className="nodrag nopan pointer-events-auto absolute flex items-center gap-1.5"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
          >
            {data?.label && (
              <span className="rounded-full border border-border bg-surface-2 px-2 py-0.5 font-mono text-[10.5px] tabular-nums text-muted-foreground">
                {data.label}
              </span>
            )}
            {canInsert && (
              <AddNodeButton
                label={insertLabel}
                onClick={() => onInsert({ edgeId: id, source, target, sourceHandle: sourceHandleId ?? null })}
              />
            )}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}
