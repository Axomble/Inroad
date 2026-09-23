import { defineFlowNodeTypes, NO_OUTPUTS, SINGLE_OUTPUT } from '@/components/shared/flow/node-registry'
import { StartNode, StepNode, StopNode } from './sequence-flow-nodes'

/**
 * The sequence canvas's node kinds. Branching adds one entry here —
 * `condition: { component: ConditionNode, size, outputs: ['yes', 'no'] }` —
 * and `buildSequenceGraph` starts emitting it; layout, handles and the Stop
 * after each unwired branch all follow from this entry.
 *
 * Module scope, built once: React Flow re-mounts every node when `nodeTypes`
 * changes identity.
 */
export const sequenceNodeRegistry = defineFlowNodeTypes({
  start: { component: StartNode, size: { width: 132, height: 36 }, outputs: SINGLE_OUTPUT, hasInput: false },
  step: { component: StepNode, size: { width: 288, height: 116 }, outputs: SINGLE_OUTPUT },
  stop: { component: StopNode, size: { width: 112, height: 34 }, outputs: NO_OUTPUTS },
})
