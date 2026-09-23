import { useMemo } from 'react'
import { graphlib, layout } from '@dagrejs/dagre'
import type { Edge, Node } from '@xyflow/react'
import type { FlowNodeRegistry, FlowNodeSize } from './node-registry'

/** The part of a registry layout needs; `handlesOf` is optional so a test can lay out bare boxes. */
export type FlowNodeShapes = Pick<FlowNodeRegistry<string>, 'sizeOf'> &
  Partial<Pick<FlowNodeRegistry<string>, 'handlesOf'>>

export type FlowLayoutOptions = {
  /** Top-to-bottom reads like a sequence; left-to-right is there for wide flows. */
  direction?: 'TB' | 'LR'
  /** Gap between siblings in the same rank (e.g. a condition's two branches). */
  nodeSpacing?: number
  /** Gap between ranks — leaves room for the edge's wait chip and add button. */
  rankSpacing?: number
}

const DEFAULTS: Required<FlowLayoutOptions> = { direction: 'TB', nodeSpacing: 56, rankSpacing: 72 }

/**
 * Positions every node with dagre and returns new nodes; the inputs are not
 * touched. Positions are React Flow's top-left corner (dagre reports centres),
 * and each node gets its declared `width`/`height` (and handle geometry) so
 * React Flow renders it at the size the layout assumed instead of waiting to
 * measure it.
 *
 * A node's children keep the order of its edges: emit a condition's "yes"
 * edge before its "no" edge and "yes" lands on the left. dagre's own crossing
 * minimisation would otherwise pick either side, so this is passed to it as an
 * explicit ordering constraint.
 */
export function layoutFlow<N extends Node>(
  nodes: readonly N[],
  edges: readonly Edge[],
  { sizeOf, handlesOf }: FlowNodeShapes,
  options: FlowLayoutOptions = {},
): N[] {
  // `??` per field, not an object spread: an explicit `undefined` must fall back
  // to the default rather than override it.
  const direction = options.direction ?? DEFAULTS.direction
  const nodeSpacing = options.nodeSpacing ?? DEFAULTS.nodeSpacing
  const rankSpacing = options.rankSpacing ?? DEFAULTS.rankSpacing
  // dagre writes each node's centre (`x`, `y`) back onto the label we give it.
  const graph = new graphlib.Graph<object, FlowNodeSize & { x?: number; y?: number }, object>()
  graph.setGraph({ rankdir: direction, nodesep: nodeSpacing, ranksep: rankSpacing })
  graph.setDefaultEdgeLabel(() => ({}))

  for (const node of nodes) graph.setNode(node.id, { ...sizeOf(node.type) })
  for (const edge of edges) {
    // An edge to a node that isn't in the graph would make dagre invent an
    // unsized node for it; drop it here rather than lay out a phantom.
    if (graph.hasNode(edge.source) && graph.hasNode(edge.target)) graph.setEdge(edge.source, edge.target)
  }

  layout(graph, { constraints: siblingOrder(edges, graph.hasNode.bind(graph)) })

  return nodes.map((node) => {
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
}

/** Left-to-right constraints between consecutive children of each node. */
function siblingOrder(edges: readonly Edge[], exists: (id: string) => boolean): { left: string; right: string }[] {
  const children = new Map<string, string[]>()
  for (const edge of edges) {
    if (!exists(edge.source) || !exists(edge.target)) continue
    const list = children.get(edge.source) ?? []
    list.push(edge.target)
    children.set(edge.source, list)
  }
  const constraints: { left: string; right: string }[] = []
  for (const targets of children.values()) {
    targets.reduce((left, right) => {
      constraints.push({ left, right })
      return right
    })
  }
  return constraints
}

/** `layoutFlow`, recomputed only when the graph itself changes. */
export function useFlowLayout<N extends Node>(
  nodes: readonly N[],
  edges: readonly Edge[],
  shapes: FlowNodeShapes,
  options?: FlowLayoutOptions,
): N[] {
  const direction = options?.direction
  const nodeSpacing = options?.nodeSpacing
  const rankSpacing = options?.rankSpacing
  return useMemo(
    () => layoutFlow(nodes, edges, shapes, { direction, nodeSpacing, rankSpacing }),
    [nodes, edges, shapes, direction, nodeSpacing, rankSpacing],
  )
}
