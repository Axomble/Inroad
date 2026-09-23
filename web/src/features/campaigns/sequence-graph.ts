// The campaign sequence as a flow graph, and the step-order arithmetic the
// canvas's gestures reduce to. Pure: no React, no React Flow rendering — the
// canvas lays out and draws whatever this returns.
//
// Routing follows the server's send path (internal/platform/seqgraph `After`):
// a step WITH a branch goes to its condition node, whose yes / no exits name a
// step or end the path; a step WITHOUT one falls through to the next step in
// order. That fall-through is computed from the order on screen rather than
// the graph endpoint's `default_next_step_id`, so a reorder whose refetch is
// still in flight draws consistently. An exit naming a step that isn't in the
// list ends the path, exactly as the send path treats it.
//
// The ORDER half below (`placeAfter`, `moveStep`, `orderForConnection`) edits
// the one ordered list the reorder endpoint takes. With conditions present it
// still means "move this step"; the fall-through edges follow the new order.
import type { Edge, Node } from '@xyflow/react'
import type { FlowEdgeType } from '@/components/shared/flow/flow-edge'
import { openEnds, type FlowHandleId, type FlowOutput } from '@/components/shared/flow/node-registry'
import type { CampaignGraphNode, StepBranch } from './api'
import type { StepWithId } from './step-card'
import { waitLabel } from './step-delay'

export const START_NODE_ID = 'start'

/** The condition node that hangs off `stepId`. */
const CONDITION_PREFIX = 'cond:'
export const conditionNodeId = (stepId: string) => `${CONDITION_PREFIX}${stepId}`

export type StartFlowNode = Node<{ stepCount: number; canModifyStructure: boolean }, 'start'>
export type StepFlowNode = Node<
  {
    step: StepWithId
    /** 1-based. */
    position: number
    /** Step 1's subject: a blank follow-up sends as "Re: <this>". */
    threadSubject: string | undefined
    canModifyStructure: boolean
    /** Conditions can be added here: the graph loaded (status doesn't matter). */
    canEditBranches: boolean
    /** A condition already hangs off this step. */
    hasBranch: boolean
    /** Some path from Start reaches it. */
    reachable: boolean
    /** On the loop the server just refused. */
    inLoop: boolean
  },
  'step'
>
export type ConditionFlowData = {
  branch: StepBranch
  /** The source step's 1-based position, for labels. */
  position: number
  inLoop: boolean
}
/** `always` is its own kind: one exit, no yes / no. */
export type ConditionFlowNode = Node<ConditionFlowData, 'condition'> | Node<ConditionFlowData, 'always'>
export type StopFlowNode = Node<Record<string, never>, 'stop'>
export type SequenceFlowNode = StartFlowNode | StepFlowNode | ConditionFlowNode | StopFlowNode

export type SequenceGraph = { nodes: SequenceFlowNode[]; edges: FlowEdgeType[] }

export type SequenceGraphOptions = {
  canModifyStructure: boolean
  /** False when the graph couldn't be loaded: branches are unknown, so none may be written. */
  canEditBranches: boolean
  /** Branches by source step id. Empty for a linear sequence. */
  branches: ReadonlyMap<string, StepBranch>
  loopStepIds: ReadonlySet<string>
  outputsOf: (type: string | undefined) => readonly FlowOutput[]
}

// Layout replaces these; React Flow's Node type requires a position up front.
const ORIGIN = { x: 0, y: 0 }

/**
 * Builds the graph for steps already sorted by `step_order`. The "+" insert
 * button is offered only on fall-through edges (Start → first, a step without
 * a condition → its next, or → Stop): inserting there means "the next step in
 * order", which is exactly what create-then-reorder produces. A condition's
 * exits are set by choosing a step, not by inserting.
 */
export function buildSequenceGraph(steps: readonly StepWithId[], options: SequenceGraphOptions): SequenceGraph {
  const { canModifyStructure, canEditBranches, branches, loopStepIds, outputsOf } = options
  const byId = new Map(steps.map((step) => [step.id, step]))
  const threadSubject = steps[0]?.subject
  const first = steps[0]

  const edges: FlowEdgeType[] = []
  if (first) {
    edges.push(
      edge(START_NODE_ID, first.id, {
        label: waitLabel(first.delay_seconds),
        insertLabel: canModifyStructure ? insertLabelAfter(0) : undefined,
      }),
    )
  }

  const conditions = new Map<string, ConditionFlowNode>()
  steps.forEach((step, index) => {
    const branch = branches.get(step.id)
    if (!branch) {
      const next = steps[index + 1]
      if (next) {
        edges.push(
          edge(step.id, next.id, {
            label: waitLabel(next.delay_seconds),
            insertLabel: canModifyStructure ? insertLabelAfter(index + 1) : undefined,
          }),
        )
      }
      return
    }
    const id = conditionNodeId(step.id)
    const data: ConditionFlowData = { branch, position: index + 1, inLoop: loopStepIds.has(step.id) }
    conditions.set(
      step.id,
      branch.condition === 'always'
        ? { id, type: 'always', position: ORIGIN, data }
        : { id, type: 'condition', position: ORIGIN, data },
    )
    edges.push(edge(step.id, id, {}))
    // Yes before no: the layout keeps sibling order, so "Yes" lands on the left.
    const exits: ['yes' | 'no', string | null][] = [['yes', branch.yes_step_id]]
    if (branch.condition !== 'always') exits.push(['no', branch.no_step_id])
    for (const [handle, target] of exits) {
      const targetStep = target ? byId.get(target) : undefined
      if (targetStep) {
        edges.push(edge(id, targetStep.id, { label: waitLabel(targetStep.delay_seconds), sourceHandle: handle }))
      }
    }
  })

  const reachable = reachableFrom(first?.id, edges)
  const nodes: SequenceFlowNode[] = [
    { id: START_NODE_ID, type: 'start', position: ORIGIN, data: { stepCount: steps.length, canModifyStructure } },
  ]
  steps.forEach((step, index) => {
    nodes.push({
      id: step.id,
      type: 'step',
      position: ORIGIN,
      data: {
        step,
        position: index + 1,
        threadSubject,
        canModifyStructure,
        canEditBranches,
        hasBranch: conditions.has(step.id),
        reachable: reachable.has(step.id),
        inLoop: loopStepIds.has(step.id),
      },
    })
    const condition = conditions.get(step.id)
    if (condition) nodes.push(condition)
  })

  for (const end of openEnds(nodes, edges, outputsOf)) {
    const stopId = `stop:${end.nodeId}:${end.handle ?? 'out'}`
    const stepIndex = steps.findIndex((step) => step.id === end.nodeId)
    // Only Start's or a step's own open end is a fall-through; a condition's
    // open exit is set in the condition editor (or by dragging), never by
    // inserting.
    const insertable = canModifyStructure && (end.nodeId === START_NODE_ID || stepIndex >= 0)
    nodes.push({ id: stopId, type: 'stop', position: ORIGIN, data: {} })
    edges.push(
      edge(end.nodeId, stopId, {
        insertLabel: insertable ? insertLabelAfter(stepIndex + 1) : undefined,
        sourceHandle: end.handle,
      }),
    )
  }

  return { nodes, edges }
}

/** Every node some path from `entryId` reaches (through condition nodes). */
function reachableFrom(entryId: string | undefined, edges: readonly FlowEdgeType[]): Set<string> {
  const out = new Map<string, string[]>()
  for (const { source, target } of edges) out.set(source, [...(out.get(source) ?? []), target])
  const seen = new Set<string>()
  const queue = entryId ? [entryId] : []
  for (let next = queue.shift(); next !== undefined; next = queue.shift()) {
    if (seen.has(next)) continue
    seen.add(next)
    queue.push(...(out.get(next) ?? []))
  }
  return seen
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
    id: sourceHandle === null ? `${source}->${target}` : `${source}:${sourceHandle}->${target}`,
    source,
    target,
    type: 'flow',
    sourceHandle,
    data,
  }
}

/** The branches in a graph response, keyed by the step they hang off. */
export function branchesByStep(nodes: readonly Pick<CampaignGraphNode, 'step_id' | 'branch'>[] | undefined): Map<string, StepBranch> {
  const map = new Map<string, StepBranch>()
  for (const node of nodes ?? []) if (node.branch) map.set(node.step_id, node.branch)
  return map
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

/** A drag from a condition's exit onto a step: "set this exit to that step". */
export type ExitConnection = { stepId: string; exit: 'yes' | 'no'; target: string }

/**
 * Reads a connection as setting a condition exit, or `null` when it isn't one
 * that could be saved: the source must be a condition node on a step that has
 * a branch, the handle one that kind has ("always" has no "no"), and the
 * target another step — an exit back to its own step is a loop the server
 * refuses outright.
 */
export function exitForConnection(
  connection: Pick<Edge, 'source' | 'target' | 'sourceHandle'>,
  branches: ReadonlyMap<string, StepBranch>,
  stepIds: ReadonlySet<string>,
): ExitConnection | null {
  const { source, target, sourceHandle } = connection
  if (!source.startsWith(CONDITION_PREFIX)) return null
  const stepId = source.slice(CONDITION_PREFIX.length)
  const branch = branches.get(stepId)
  if (!branch || !stepIds.has(target) || target === stepId) return null
  if (sourceHandle === 'yes') return { stepId, exit: 'yes', target }
  if (sourceHandle === 'no' && branch.condition !== 'always') return { stepId, exit: 'no', target }
  return null
}

/**
 * Step 1 opens the email thread, so it must carry a subject — the same rule
 * `StepForm` enforces when a step is added at the start. A blank-subject
 * follow-up ("Re: <step 1>") must never be moved into first place.
 */
export function leadsWithSubject(order: readonly string[], subjectOf: (id: string) => string | undefined): boolean {
  const first = order[0]
  return first === undefined || (subjectOf(first)?.trim() ?? '') !== ''
}

/**
 * `steps` rearranged into `order`. Only when both name exactly the same steps —
 * an order from before a step was added or deleted says nothing reliable about
 * the list as it is now, so the list is returned unchanged.
 */
export function applyOrder<S extends { id: string }>(steps: readonly S[], order: readonly string[]): readonly S[] {
  const byId = new Map(steps.map((step) => [step.id, step]))
  if (order.length !== steps.length || order.some((id) => !byId.has(id))) return steps
  return order.flatMap((id) => {
    const step = byId.get(id)
    return step ? [step] : []
  })
}

/** True when the order actually differs — a no-op gesture shouldn't hit the API. */
export function orderChanged(before: readonly string[], after: readonly string[]): boolean {
  return before.length !== after.length || before.some((id, index) => after[index] !== id)
}
