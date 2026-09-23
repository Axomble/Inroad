import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  type Edge,
  type EdgeProps,
  type Position,
  type XYPosition,
} from '@xyflow/react'
import { AddNodeButton } from './add-node-button'
import { useFlowInsert } from './flow-insert-context'

export type FlowEdgeData = {
  /** A chip on the edge — what happens between the two nodes ("Wait 3 days"). */
  label?: string
  /** Present = the edge offers an add button; the text is that button's accessible name. */
  insertLabel?: string
  /**
   * Bend points the layout routed the edge through (see `layoutFlow`), for an
   * edge that skips a rank and would otherwise be drawn behind a node.
   */
  route?: readonly XYPosition[]
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
  const [path, labelX, labelY] = edgePath({
    from: { x: sourceX, y: sourceY },
    to: { x: targetX, y: targetY },
    route: data?.route ?? [],
    sourcePosition,
    targetPosition,
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
                onClick={() => onInsert({ source, sourceHandle: sourceHandleId ?? null })}
              />
            )}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

/**
 * The edge's path and where its chip sits. Without a route it is one rounded
 * orthogonal step; with one, a step through each bend point in turn (a path of
 * several subpaths reads as one line, and the arrowhead lands on the last
 * vertex), with the chip on the middle bend so it sits in the gap the route
 * found rather than on a node.
 */
function edgePath({
  from,
  to,
  route,
  sourcePosition,
  targetPosition,
}: {
  from: XYPosition
  to: XYPosition
  route: readonly XYPosition[]
  sourcePosition: Position
  targetPosition: Position
}): [path: string, labelX: number, labelY: number] {
  const waypoints = [from, ...route, to]
  const segments = waypoints.slice(1).map((end, index) => {
    const start = waypoints[index] ?? from
    return getSmoothStepPath({
      sourceX: start.x,
      sourceY: start.y,
      targetX: end.x,
      targetY: end.y,
      sourcePosition,
      targetPosition,
      borderRadius: 10,
    })
  })
  const path = segments.map(([segment]) => segment).join(' ')
  const middle = route[Math.floor(route.length / 2)]
  if (middle) return [path, middle.x, middle.y]
  const [, labelX, labelY] = segments[0] ?? ['', from.x, from.y]
  return [path, labelX, labelY]
}
