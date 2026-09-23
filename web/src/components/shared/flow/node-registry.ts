import { Position, type Edge, type Node, type NodeHandle, type NodeTypes } from '@xyflow/react'

/**
 * A source-handle id. `null` is React Flow's unnamed default handle — what a
 * node with a single way out uses. A node with several ways out (a condition's
 * "yes" and "no") names each one, and every edge leaving it carries the name as
 * its `sourceHandle`.
 */
export type FlowHandleId = string | null

/** The single-exit handle list, shared so a registry entry and its node agree. */
export const SINGLE_OUTPUT: readonly FlowHandleId[] = [null]

/** No way out: the end of a path. */
export const NO_OUTPUTS: readonly FlowHandleId[] = []

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
   * The node's exits, in left-to-right order. Drives both where the node
   * renders its source handles and which exits `openEnds` reports as unwired.
   */
  outputs: readonly FlowHandleId[]
  /** `false` for an entry point (Start, Trigger): nothing leads into it. Defaults to `true`. */
  hasInput?: boolean
}

export type FlowNodeRegistry<K extends string> = {
  /** Stable map for React Flow's `nodeTypes` prop — build the registry once, at module scope. */
  nodeTypes: NodeTypes
  sizeOf: (type: string | undefined) => FlowNodeSize
  outputsOf: (type: string | undefined) => readonly FlowHandleId[]
  handlesOf: (type: string | undefined) => NodeHandle[]
  types: readonly K[]
}

/**
 * Declares a canvas's node kinds in one place. Adding a kind of node is adding
 * an entry here; the layout, the open-end detection and React Flow's renderer
 * all read from the same definition, so they cannot drift apart.
 */
export function defineFlowNodeTypes<K extends string>(defs: Record<K, FlowNodeTypeDef>): FlowNodeRegistry<K> {
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
    handlesOf: (type) => handleGeometry(lookup(type)),
    types: entries.map(([key]) => key),
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
  const handles: NodeHandle[] = outputs.map((id, index) => ({
    id,
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
  outputsOf: (type: string | undefined) => readonly FlowHandleId[],
): OpenEnd[] {
  const wired = new Set(edges.map((edge) => handleKey(edge.source, edge.sourceHandle ?? null)))
  return nodes.flatMap((node) =>
    outputsOf(node.type)
      .filter((handle) => !wired.has(handleKey(node.id, handle)))
      .map((handle) => ({ nodeId: node.id, handle })),
  )
}

function handleKey(nodeId: string, handle: FlowHandleId): string {
  return `${nodeId}\u0000${handle ?? ''}`
}
