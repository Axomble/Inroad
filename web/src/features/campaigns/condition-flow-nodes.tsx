import type { NodeProps } from '@xyflow/react'
import { FlowNodeFrame } from '@/components/shared/flow/flow-node-frame'
import { cn } from '@/lib/utils'
// Read-only cross-feature hook (the documented exception): a condition node
// names its reply label by the label's display name, which the reply-labels
// feature owns. Hooks only — no reply-labels UI or state is imported.
import { useListReplyLabelsQuery } from '@/features/reply-labels/api'
import { describeBranch } from './branch-draft'
import { focusKey, useFocusRequest, useSequenceCanvasActions } from './sequence-canvas-actions'
import type { ConditionFlowNode } from './sequence-graph'
import { NodeFlag } from './node-flag'

// The shapes, in a 0–100 box stretched to the node. The condition's diamond is
// cut flat at the bottom so its two exits — at 1/3 and 2/3 of the width, where
// the registry puts the handles — leave from its corners rather than floating
// under a slanted edge.
const CONDITION_SHAPE = '50,1 99,50 66.7,99 33.3,99 1,50'
const ALWAYS_SHAPE = '50,1 99,50 50,99 1,50'

/**
 * The IF after a step: its condition in words, "Yes" / "No" under its exits
 * (drawn by the frame from the registry's output labels), and the whole face
 * one button that opens the condition editor.
 */
export function ConditionNode(props: NodeProps<ConditionFlowNode>) {
  return <RouterNode {...props} shape={CONDITION_SHAPE} eyebrow="If" />
}

/** An unconditional jump: "always go to …". One exit. */
export function AlwaysNode(props: NodeProps<ConditionFlowNode>) {
  return <RouterNode {...props} shape={ALWAYS_SHAPE} eyebrow="Then" />
}

function RouterNode({
  type,
  data,
  shape,
  eyebrow,
}: NodeProps<ConditionFlowNode> & { shape: string; eyebrow: string }) {
  const { branch, position, inLoop } = data
  const actions = useSequenceCanvasActions()
  const ref = useFocusRequest<HTMLButtonElement>(focusKey(branch.step_id, 'condition'))
  const labelKey = branch.reply_label_key
  const { data: labels } = useListReplyLabelsQuery(undefined, { skip: !labelKey })
  const labelName = labels?.labels.find((label) => label.key === labelKey)?.label
  const summary = describeBranch(branch, labelName)
  const isEditing = actions.editingConditionStepId === branch.step_id

  return (
    // Conditions change routing, not structure, so their exits can be dragged
    // on a running campaign too.
    <FlowNodeFrame type={type} outputsConnectable>
      <svg
        className="pointer-events-none absolute inset-0 h-full w-full overflow-visible"
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <polygon
          points={shape}
          vectorEffect="non-scaling-stroke"
          className={cn(
            'fill-surface',
            inLoop ? 'stroke-danger' : isEditing ? 'stroke-ring' : 'stroke-border-strong',
          )}
          strokeWidth={inLoop || isEditing ? 2 : 1.25}
        />
      </svg>
      <button
        ref={ref}
        type="button"
        aria-label={`Edit the condition after step ${position}: ${summary}`}
        aria-current={isEditing ? 'true' : undefined}
        onClick={() => actions.editCondition(branch.step_id)}
        className="nodrag absolute inset-x-[18%] inset-y-[14%] flex cursor-pointer flex-col items-center justify-center gap-0.5 rounded-md text-center outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex items-center gap-1">
          <span className="font-mono text-[9.5px] uppercase tracking-[0.16em] text-accent-ink">{eyebrow}</span>
          {inLoop && <NodeFlag tone="danger">In a loop</NodeFlag>}
        </span>
        <span className="line-clamp-2 text-[11.5px] font-medium leading-tight text-foreground">{summary}</span>
      </button>
    </FlowNodeFrame>
  )
}
