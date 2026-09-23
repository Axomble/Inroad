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

/** Far above any ordinary edge's default weight of 1; see the relay edges in `layoutFlow`. */
const RELAY_WEIGHT = 1000

// dagre writes each node's centre (`x`, `y`) back onto the label we give it,
// and each edge's route (`points`) onto its label.
type PlacedNode = FlowNodeSize & { x?: number; y?: number }
type PlacedEdge = { points?: XYPosition[]; weight?: number }

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
 * left all the way down. dagre can only be told the order of nodes within one
 * rank, and an exit that skips a rank has no node there — so every exit of a
 * multi-exit node is routed through a relay point in the rank right below, and
 * the relays are ordered. Without that, "yes → step 3" and "no → step 2" leave the diamond
 * crossed.
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
  const relayed = relayedEdges(placeable)
  for (const edge of placeable) {
    const relay = relayed.get(edge.id)
    if (!relay) {
      graph.setEdge(edge.source, edge.target, {}, edge.id)
      continue
    }
    graph.setNode(relay, { width: 1, height: 1 })
    // Heavy, so the ranker keeps it one rank long: every relay of a node sits
    // in the rank right below it, where the order constraint can apply. A
    // light edge lets a relay drift down towards a distant target — into a
    // different rank from its siblings — and the exits cross again.
    graph.setEdge(edge.source, relay, { weight: RELAY_WEIGHT }, `${edge.id}:in`)
    graph.setEdge(relay, edge.target, {}, `${edge.id}:out`)
  }

  // `disableOptimalOrderHeuristic` keeps dagre's initial order: a depth-first
  // walk that visits each node's edges in the order given — exactly "yes before
  // no", step by step. Its crossing-reduction sweeps were observed to undo that
  // one rank below the relays (putting the "no" target left of the "yes"
  // path's dummy node), re-crossing exits the constraint had just uncrossed.
  // The flows drawn here are near-trees, where the walk order is already
  // crossing-free.
  layout(graph, { constraints: relayOrder(placeable, relayed), disableOptimalOrderHeuristic: true })

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
    const relay = relayed.get(edge.id)
    if (relay) {
      const { x, y } = graph.node(relay)
      const onward = graph.edge({ v: relay, w: edge.target, name: `${edge.id}:out` })?.points ?? []
      const route = [{ x: x ?? 0, y: y ?? 0 }, ...onward.slice(1, -1)]
      return { ...edge, data: { ...edge.data, route } }
    }
    const points = graph.edge({ v: edge.source, w: edge.target, name: edge.id })?.points ?? []
    // Three points is dagre's route for adjacent ranks (leave, middle, arrive):
    // nothing is in the way, and the edge's own orthogonal path is cleaner.
    // Anything longer passed through dummy nodes between the ranks.
    if (points.length <= 3) return edge
    return { ...edge, data: { ...edge.data, route: points.slice(1, -1) } }
  })
  return { nodes: laidNodes, edges: laidEdges }
}

/** Relay node ids, by edge, for every edge leaving a node that has more than one. */
function relayedEdges(edges: readonly FlowEdgeType[]): Map<string, string> {
  const outgoing = new Map<string, number>()
  for (const edge of edges) outgoing.set(edge.source, (outgoing.get(edge.source) ?? 0) + 1)
  const relays = new Map<string, string>()
  for (const edge of edges) {
    // A NUL never appears in a node id a caller chooses, so a relay id can't
    // collide with a real node.
    if ((outgoing.get(edge.source) ?? 0) > 1) relays.set(edge.id, `\u0000relay\u0000${edge.id}`)
  }
  return relays
}

/** Left-to-right constraints between consecutive relays of each node, in edge order. */
function relayOrder(edges: readonly FlowEdgeType[], relays: ReadonlyMap<string, string>): { left: string; right: string }[] {
  const bySource = new Map<string, string[]>()
  for (const edge of edges) {
    const relay = relays.get(edge.id)
    if (relay) bySource.set(edge.source, [...(bySource.get(edge.source) ?? []), relay])
  }
  const constraints: { left: string; right: string }[] = []
  for (const ordered of bySource.values()) {
    ordered.reduce((left, right) => {
      constraints.push({ left, right })
      return right
    })
  }
  return constraints
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
