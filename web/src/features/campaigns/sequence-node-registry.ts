import { defineFlowNodeTypes, NO_OUTPUTS, SINGLE_OUTPUT } from '@/components/shared/flow/node-registry'
import { AlwaysNode, ConditionNode } from './condition-flow-nodes'
import { StartNode, StepNode, StopNode } from './sequence-flow-nodes'

/**
 * The sequence canvas's node kinds. A condition is not a row of its own in the
 * database — it is the router attached to one step — but it is drawn as its own
 * node right after that step, so its two exits have somewhere to leave from.
 *
 * `always` is a separate kind rather than a condition with a dead "No" handle:
 * the contract gives it exactly one exit (`yes_step_id`; `no_step_id` must be
 * null), and a handle nothing may connect to would be a lie on the canvas.
 *
 * Module scope, built once: React Flow re-mounts every node when `nodeTypes`
 * changes identity.
 */
export const sequenceNodeRegistry = defineFlowNodeTypes({
  start: { component: StartNode, size: { width: 132, height: 36 }, outputs: SINGLE_OUTPUT, hasInput: false },
  step: { component: StepNode, size: { width: 288, height: 116 }, outputs: SINGLE_OUTPUT },
  condition: {
    component: ConditionNode,
    size: { width: 228, height: 96 },
    outputs: [
      { id: 'yes', label: 'Yes' },
      { id: 'no', label: 'No' },
    ],
  },
  always: { component: AlwaysNode, size: { width: 176, height: 72 }, outputs: [{ id: 'yes' }] },
  stop: { component: StopNode, size: { width: 112, height: 34 }, outputs: NO_OUTPUTS },
})
