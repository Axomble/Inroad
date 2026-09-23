import { Position, type Edge, type Node, type NodeHandle, type NodeTypes } from '@xyflow/react'

/**
 * A source-handle id. `null` is React Flow's unnamed default handle — what a
 * node with a single way out uses. A node with several ways out (a condition's
 * "yes" and "no") names each one, and every edge leaving it carries the name as
 * its `sourceHandle`.
 */
export type FlowHandleId = string | null

/**
 * One way out of a node. The label travels with the id, so an exit can't be
 * drawn with a name that belongs to another; it is rendered as text beside the
 * handle, so branches are never told apart by position or colour alone.
 */
export type FlowOutput = { id: FlowHandleId; label?: string }

/** The single-exit output list, shared so every single-exit kind agrees. */
export const SINGLE_OUTPUT: readonly FlowOutput[] = [{ id: null }]

/** No way out: the end of a path. */
export const NO_OUTPUTS: readonly FlowOutput[] = []

export type FlowNodeSize = { width: number; height: number }

/**
 * Everything the canvas needs to know about one kind of node. The size is fixed
 * rather than measured because dagre lays the graph out before anything is in
 * the DOM; a node component must render inside the box it declares.
 */
export type FlowNodeTypeDef = {
  component: NodeTypes[string]
  size: FlowNodeSize
  /**
   * The node's exits, in left-to-right order. Drives where `FlowNodeFrame`
   * draws the source handles, where React Flow thinks they are, and which
   * exits `openEnds` reports as unwired.
   */
  outputs: readonly FlowOutput[]
  /** `false` for an entry point (Start, Trigger): nothing leads into it. Defaults to `true`. */
  hasInput?: boolean
}

export type FlowNodeRegistry = {
  /** Stable map for React Flow's `nodeTypes` prop — build the registry once, at module scope. */
  nodeTypes: NodeTypes
  sizeOf: (type: string | undefined) => FlowNodeSize
  outputsOf: (type: string | undefined) => readonly FlowOutput[]
  hasInputOf: (type: string | undefined) => boolean
  handlesOf: (type: string | undefined) => NodeHandle[]
}

/**
 * Declares a canvas's node kinds in one place. The layout, the handle geometry
 * React Flow routes edges by, the handles `FlowNodeFrame` draws, and the
 * open-end detection all read the same definition, so they cannot drift apart.
 */
export function defineFlowNodeTypes<K extends string>(defs: Record<K, FlowNodeTypeDef>): FlowNodeRegistry {
  const entries = Object.entries(defs) as [K, FlowNodeTypeDef][]
  const lookup = (type: string | undefined): FlowNodeTypeDef => {
    const found = entries.find(([key]) => key === type)
    // A node whose type nobody registered would render as React Flow's default
    // box and lay out at a guessed size. That is a programming error, not a
    // state to paper over.
    if (!found) throw new Error(`flow: no node type registered for "${String(type)}"`)
    return found[1]
  }
  return {
    nodeTypes: Object.fromEntries(entries.map(([key, def]) => [key, def.component])),
    sizeOf: (type) => lookup(type).size,
    outputsOf: (type) => lookup(type).outputs,
    hasInputOf: (type) => lookup(type).hasInput ?? true,
    handlesOf: (type) => handleGeometry(lookup(type)),
  }
}

/** Rendered handle size (see flow.css); geometry is the handle's top-left corner. */
const HANDLE_SIZE = 9

/**
 * Where a node's handles sit, computed from its fixed size — the same spots
 * `FlowNodeFrame` draws them (entry top-centre, exits spread evenly along the
 * bottom). Handing these to React Flow up front means edges draw on the first
 * frame instead of after every handle has been measured in the DOM.
 */
function handleGeometry({ size, outputs, hasInput = true }: FlowNodeTypeDef): NodeHandle[] {
  const half = HANDLE_SIZE / 2
  const handles: NodeHandle[] = outputs.map((output, index) => ({
    id: output.id,
    type: 'source',
    position: Position.Bottom,
    x: outputOffset(index, outputs.length) * size.width - half,
    y: size.height - half,
    width: HANDLE_SIZE,
    height: HANDLE_SIZE,
  }))
  if (hasInput) {
    handles.unshift({
      id: null,
      type: 'target',
      position: Position.Top,
      x: size.width / 2 - half,
      y: -half,
      width: HANDLE_SIZE,
      height: HANDLE_SIZE,
    })
  }
  return handles
}

/** Fraction of the node's width at which exit `index` of `count` sits. */
export function outputOffset(index: number, count: number): number {
  return (index + 1) / (count + 1)
}

export type OpenEnd = { nodeId: string; handle: FlowHandleId }

/**
 * Every exit that no edge leaves from. A flow never ends implicitly: whatever
 * builds the graph appends a terminal node to each of these, so a path that
 * goes nowhere is drawn as ending rather than silently dangling.
 */
export function openEnds(
  nodes: readonly Node[],
  edges: readonly Edge[],
  outputsOf: (type: string | undefined) => readonly FlowOutput[],
): OpenEnd[] {
  const wired = new Set(edges.map((edge) => handleKey(edge.source, edge.sourceHandle ?? null)))
  return nodes.flatMap((node) =>
    outputsOf(node.type)
      .filter((output) => !wired.has(handleKey(node.id, output.id)))
      .map((output) => ({ nodeId: node.id, handle: output.id })),
  )
}

function handleKey(nodeId: string, handle: FlowHandleId): string {
  return `${nodeId}\u0000${handle ?? ''}`
}
