// The campaign sequence as a flow graph, and the step-order arithmetic the
// canvas's gestures reduce to. Pure: no React, no React Flow rendering — the
// canvas lays out and draws whatever this returns.
//
// Today a sequence is linear (Start → step 1 → … → step N → Stop). Branching
// adds a `condition` node kind whose two exits carry `sourceHandle: 'yes' |
// 'no'`; the only change here is emitting those nodes and edges from the
// branching data. Stop nodes already come from `openEnds`, so every branch
// that leads nowhere gets its own Stop without this file knowing about it.
import type { Edge, Node } from '@xyflow/react'
import type { FlowEdgeType } from '@/components/shared/flow/flow-edge'
import { openEnds, type FlowHandleId } from '@/components/shared/flow/node-registry'
import type { StepWithId } from './step-card'
import { waitLabel } from './step-delay'

export const START_NODE_ID = 'start'

export type StartFlowNode = Node<{ stepCount: number }, 'start'>
export type StepFlowNode = Node<
  {
    step: StepWithId
    /** 1-based. */
    position: number
    stepCount: number
    /** Step 1's subject: a blank follow-up sends as "Re: <this>". */
    threadSubject: string | undefined
    canModifyStructure: boolean
  },
  'step'
>
export type StopFlowNode = Node<Record<string, never>, 'stop'>
export type SequenceFlowNode = StartFlowNode | StepFlowNode | StopFlowNode

export type SequenceGraph = { nodes: SequenceFlowNode[]; edges: FlowEdgeType[] }

// Layout replaces these; React Flow's Node type requires a position up front.
const ORIGIN = { x: 0, y: 0 }

/**
 * Builds the graph for steps already sorted by `step_order`. Every edge offers
 * an add button while structure is editable — including the one into Stop,
 * which is how a step gets appended after the last.
 */
export function buildSequenceGraph(
  steps: readonly StepWithId[],
  {
    canModifyStructure,
    outputsOf,
  }: { canModifyStructure: boolean; outputsOf: (type: string | undefined) => readonly FlowHandleId[] },
): SequenceGraph {
  const threadSubject = steps[0]?.subject
  const nodes: SequenceFlowNode[] = [
    { id: START_NODE_ID, type: 'start', position: ORIGIN, data: { stepCount: steps.length } },
    ...steps.map(
      (step, index): StepFlowNode => ({
        id: step.id,
        type: 'step',
        position: ORIGIN,
        data: { step, position: index + 1, stepCount: steps.length, threadSubject, canModifyStructure },
      }),
    ),
  ]

  const edges: FlowEdgeType[] = steps.map((step, index) => {
    const source = index === 0 ? START_NODE_ID : (steps[index - 1]?.id ?? START_NODE_ID)
    return edge(source, step.id, {
      label: waitLabel(step.delay_seconds),
      insertLabel: canModifyStructure ? insertLabelAfter(index) : undefined,
    })
  })

  for (const end of openEnds(nodes, edges, outputsOf)) {
    const stopId = `stop:${end.nodeId}:${end.handle ?? 'out'}`
    const afterIndex = steps.findIndex((step) => step.id === end.nodeId)
    nodes.push({ id: stopId, type: 'stop', position: ORIGIN, data: {} })
    edges.push(
      edge(end.nodeId, stopId, {
        insertLabel: canModifyStructure ? insertLabelAfter(afterIndex + 1) : undefined,
        sourceHandle: end.handle,
      }),
    )
  }

  return { nodes, edges }
}

/** `position` counts steps before the gap: 0 is right after Start. */
function insertLabelAfter(position: number): string {
  return position === 0 ? 'Add a step at the start' : `Add a step after step ${position}`
}

function edge(
  source: string,
  target: string,
  { label, insertLabel, sourceHandle = null }: { label?: string; insertLabel?: string; sourceHandle?: FlowHandleId },
): FlowEdgeType {
  const data: FlowEdgeType['data'] = {}
  if (label !== undefined) data.label = label
  if (insertLabel !== undefined) data.insertLabel = insertLabel
  return {
    id: `${source}->${target}`,
    source,
    target,
    type: 'flow',
    sourceHandle,
    data,
  }
}

// --- Step order -----------------------------------------------------------
//
// The reorder endpoint takes the FULL ordered id list, so every canvas gesture
// resolves to "the whole order, changed like this". `null` for `afterId` means
// "right after Start", i.e. first.

/** Places `id` directly after `afterId` (removing it from wherever it was). */
export function placeAfter(order: readonly string[], afterId: string | null, id: string): string[] {
  const rest = order.filter((existing) => existing !== id)
  const at = afterId === null ? 0 : rest.indexOf(afterId) + 1
  // An anchor that isn't in the list is a stale gesture; leave the order alone
  // rather than silently moving the step to the front.
  if (afterId !== null && at === 0) return [...order]
  return [...rest.slice(0, at), id, ...rest.slice(at)]
}

/** Swaps `id` with its neighbour one place up (-1) or down (+1); unchanged at either end. */
export function moveStep(order: readonly string[], id: string, delta: -1 | 1): string[] {
  const from = order.indexOf(id)
  const to = from + delta
  const neighbour = order[to]
  if (from < 0 || neighbour === undefined) return [...order]
  const next = [...order]
  next[from] = neighbour
  next[to] = id
  return next
}

/**
 * Dragging from one node's exit to a step means "that step comes next". In a
 * linear sequence that is a move: the target is placed right after the source.
 */
export function orderForConnection(order: readonly string[], source: string, target: string): string[] {
  return placeAfter(order, source === START_NODE_ID ? null : source, target)
}

/**
 * Which drags mean anything: from Start or a step, onto a different step. Stop
 * is never a target (every open end already has one) and nothing leaves it.
 */
export function isMeaningfulConnection(
  connection: Pick<Edge, 'source' | 'target'>,
  stepIds: ReadonlySet<string>,
): boolean {
  const { source, target } = connection
  if (source === target || !stepIds.has(target)) return false
  return source === START_NODE_ID || stepIds.has(source)
}

/** True when the order actually differs — a no-op gesture shouldn't hit the API. */
export function orderChanged(before: readonly string[], after: readonly string[]): boolean {
  return before.length !== after.length || before.some((id, index) => after[index] !== id)
}
