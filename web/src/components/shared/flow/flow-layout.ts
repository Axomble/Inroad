import { useMemo } from 'react'
import { graphlib, layout } from '@dagrejs/dagre'
import type { Node, XYPosition } from '@xyflow/react'
import type { FlowEdgeType } from './flow-edge'
import type { FlowNodeRegistry, FlowNodeSize } from './node-registry'

/** The part of a registry layout needs; `handlesOf` is optional so a test can lay out bare boxes. */
export type FlowNodeShapes = Pick<FlowNodeRegistry, 'sizeOf'> & Partial<Pick<FlowNodeRegistry, 'handlesOf'>>

export type FlowLayoutOptions = {
  /** Top-to-bottom reads like a sequence; left-to-right is there for wide flows. */
  direction?: 'TB' | 'LR'
  /** Gap between siblings in the same rank (e.g. a condition's two branches). */
  nodeSpacing?: number
  /** Gap between ranks — leaves room for the edge's wait chip and add button. */
  rankSpacing?: number
}

export type FlowLayout<N extends Node> = { nodes: N[]; edges: FlowEdgeType[] }

const DEFAULTS: Required<FlowLayoutOptions> = { direction: 'TB', nodeSpacing: 56, rankSpacing: 72 }

// dagre writes each node's centre (`x`, `y`) back onto the label we give it,
// and each edge's route (`points`) onto its label.
type PlacedNode = FlowNodeSize & { x?: number; y?: number }
type PlacedEdge = { points?: XYPosition[] }

/**
 * Positions every node with dagre and returns new nodes and edges; the inputs
 * are not touched. Positions are React Flow's top-left corner (dagre reports
 * centres), and each node gets its declared `width`/`height` (and handle
 * geometry) so React Flow renders it at the size the layout assumed instead of
 * waiting to measure it.
 *
 * An edge that skips a rank (a branch jumping from step 1 to step 3) gets
 * dagre's bend points as `data.route`. Drawn straight, it would run behind the
 * node in between and read as going there; dagre routes it through the gap
 * beside that node instead.
 *
 * A node with several exits keeps them in the order of its edges: emit a
 * condition's "yes" edge before its "no" edge and the yes path stays on the
 * left all the way down. That order comes from dagre's INITIAL ordering — a
 * depth-first walk that visits each node's edges in insertion order — which is
 * kept as-is (`disableOptimalOrderHeuristic`). dagre 3's crossing-reduction
 * sweeps are what would otherwise run, and they re-crossed a condition's exits
 * (yes → a far step, no → the next one); its `constraints` option can't
 * prevent that, because it only orders nodes within one rank and a skipping
 * exit has no node there (and with the sweeps off, dagre never reads
 * constraints at all). The flows drawn here are near-trees, where the walk
 * order is already crossing-free; the layout tests pin that for two
 * conditions, a converging path and a backward jump.
 */
export function layoutFlow<N extends Node>(
  nodes: readonly N[],
  edges: readonly FlowEdgeType[],
  { sizeOf, handlesOf }: FlowNodeShapes,
  options: FlowLayoutOptions = {},
): FlowLayout<N> {
  // `??` per field, not an object spread: an explicit `undefined` must fall back
  // to the default rather than override it.
  const direction = options.direction ?? DEFAULTS.direction
  const nodeSpacing = options.nodeSpacing ?? DEFAULTS.nodeSpacing
  const rankSpacing = options.rankSpacing ?? DEFAULTS.rankSpacing
  // A multigraph, so two edges between the same pair (a condition whose Yes and
  // No both go to one step) are laid out — and routed — separately.
  const graph = new graphlib.Graph<object, PlacedNode, PlacedEdge>({ multigraph: true })
  graph.setGraph({ rankdir: direction, nodesep: nodeSpacing, ranksep: rankSpacing })
  graph.setDefaultEdgeLabel(() => ({}))

  for (const node of nodes) graph.setNode(node.id, { ...sizeOf(node.type) })
  // An edge to a node that isn't in the graph would make dagre invent an
  // unsized node for it; leave it out rather than lay out a phantom.
  const placeable = edges.filter((edge) => graph.hasNode(edge.source) && graph.hasNode(edge.target))
  for (const edge of placeable) graph.setEdge(edge.source, edge.target, {}, edge.id)

  layout(graph, { disableOptimalOrderHeuristic: true })

  const laidNodes = nodes.map((node) => {
    const { width, height } = sizeOf(node.type)
    const placed = graph.node(node.id)
    return {
      ...node,
      width,
      height,
      ...(handlesOf ? { handles: handlesOf(node.type) } : {}),
      position: { x: (placed.x ?? 0) - width / 2, y: (placed.y ?? 0) - height / 2 },
    }
  })
  const laidEdges = edges.map((edge) => {
    const points = graph.edge({ v: edge.source, w: edge.target, name: edge.id })?.points ?? []
    // Three points is dagre's route for adjacent ranks (leave, middle, arrive):
    // nothing is in the way, and the edge's own orthogonal path is cleaner.
    // Anything longer passed through dummy nodes between the ranks.
    if (points.length <= 3) return edge
    return { ...edge, data: { ...edge.data, route: points.slice(1, -1) } }
  })
  return { nodes: laidNodes, edges: laidEdges }
}

/** `layoutFlow`, recomputed only when the graph itself changes. */
export function useFlowLayout<N extends Node>(
  nodes: readonly N[],
  edges: readonly FlowEdgeType[],
  shapes: FlowNodeShapes,
  options?: FlowLayoutOptions,
): FlowLayout<N> {
  const direction = options?.direction
  const nodeSpacing = options?.nodeSpacing
  const rankSpacing = options?.rankSpacing
  return useMemo(
    () => layoutFlow(nodes, edges, shapes, { direction, nodeSpacing, rankSpacing }),
    [nodes, edges, shapes, direction, nodeSpacing, rankSpacing],
  )
}

/**
 * The laid-out graph's height in flow units — what a canvas that grows with its
 * content (up to a cap) sizes itself by.
 */
export function flowContentHeight(nodes: readonly Node[]): number {
  if (nodes.length === 0) return 0
  const top = Math.min(...nodes.map((node) => node.position.y))
  const bottom = Math.max(...nodes.map((node) => node.position.y + (node.height ?? 0)))
  return bottom - top
}
