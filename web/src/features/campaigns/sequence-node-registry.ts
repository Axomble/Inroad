import { defineFlowNodeTypes, NO_OUTPUTS, SINGLE_OUTPUT } from '@/components/shared/flow/node-registry'
import { StartNode, StepNode, StopNode } from './sequence-flow-nodes'

/**
 * The sequence canvas's node kinds.
 *
 * For branching, the render/layout side is ready: an entry like
 * `condition: { component: ConditionNode, size, outputs: [{ id: 'yes', label:
 * 'Yes' }, { id: 'no', label: 'No' }] }` gets its handles drawn and routed, its
 * labels shown, its branches laid out side by side and a Stop after each
 * unwired exit. What this entry does NOT buy is the editing side — insert,
 * move, and drag-to-connect are linear-only today (see `sequence-graph.ts`).
 *
 * Module scope, built once: React Flow re-mounts every node when `nodeTypes`
 * changes identity.
 */
export const sequenceNodeRegistry = defineFlowNodeTypes({
  start: { component: StartNode, size: { width: 132, height: 36 }, outputs: SINGLE_OUTPUT, hasInput: false },
  step: { component: StepNode, size: { width: 288, height: 116 }, outputs: SINGLE_OUTPUT },
  stop: { component: StopNode, size: { width: 112, height: 34 }, outputs: NO_OUTPUTS },
})
