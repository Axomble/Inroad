import type { NodeProps } from '@xyflow/react'
import { ArrowDown, ArrowUp, CirclePlay, FlaskConical, Mail, Square, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { FlowNodeFrame } from '@/components/shared/flow/flow-node-frame'
import { NO_OUTPUTS, SINGLE_OUTPUT } from '@/components/shared/flow/node-registry'
import { cn } from '@/lib/utils'
import { useListStepVariantsQuery } from './api'
import type { StartFlowNode, StepFlowNode, StopFlowNode } from './sequence-graph'
import { useSequenceCanvasActions } from './sequence-canvas-actions'

const DRAFT_ONLY_HINT = 'Structural changes are draft-only'

const terminalClass =
  'flex h-full w-full items-center justify-center gap-2 rounded-full border border-border-strong bg-surface font-mono text-[10.5px] uppercase tracking-[0.14em] text-muted-foreground'

export function StartNode({ data, isConnectable }: NodeProps<StartFlowNode>) {
  return (
    <FlowNodeFrame hasInput={false} outputs={SINGLE_OUTPUT} connectable={isConnectable && data.stepCount > 0}>
      <div className={terminalClass}>
        <CirclePlay className="size-3.5 text-accent-ink" aria-hidden="true" />
        Start
      </div>
    </FlowNodeFrame>
  )
}

export function StopNode(_props: NodeProps<StopFlowNode>) {
  return (
    <FlowNodeFrame outputs={NO_OUTPUTS}>
      <div className={cn(terminalClass, 'text-faint')}>
        <Square className="size-3 fill-current" aria-hidden="true" />
        Stop
      </div>
    </FlowNodeFrame>
  )
}

/**
 * One email in the sequence. The body is a single button that opens the step's
 * editor; the toolbar underneath carries the rest. Everything is a real button
 * with a name, so the whole canvas is operable by Tab and Enter.
 */
export function StepNode({ data, isConnectable }: NodeProps<StepFlowNode>) {
  const { step, position, stepCount, threadSubject, canModifyStructure } = data
  const actions = useSequenceCanvasActions()
  const isEditing = actions.editingStepId === step.id
  const sameThread = !step.subject && position > 1
  const subjectLine = sameThread ? `Re: ${threadSubject || 'the previous email'}` : step.subject || 'No subject yet'
  const bodyPreview = step.body_text?.trim().replace(/\s+/g, ' ') ?? ''

  return (
    <FlowNodeFrame outputs={SINGLE_OUTPUT} connectable={isConnectable}>
      <div
        className={cn(
          'flex h-full w-full flex-col overflow-hidden rounded-lg border bg-surface shadow-sm',
          isEditing ? 'border-ring ring-2 ring-ring/40' : 'border-border-strong',
        )}
      >
        <button
          type="button"
          aria-label={`Edit step ${position}: ${subjectLine}`}
          aria-current={isEditing ? 'true' : undefined}
          onClick={() => actions.editStep(step.id)}
          className="nodrag flex min-h-0 flex-1 cursor-pointer flex-col items-start gap-1 px-3 pt-2.5 pb-2 text-left outline-none hover:bg-surface-2/60 focus-visible:bg-surface-2/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
        >
          <span className="flex w-full items-center gap-2">
            <Mail className="size-3.5 text-faint" aria-hidden="true" />
            <span className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">Step {position}</span>
            <VariantCount campaignId={actions.campaignId} stepId={step.id} />
          </span>
          <span className="flex w-full min-w-0 items-center gap-2">
            <span
              className={cn(
                'truncate text-[13px] font-medium',
                sameThread || !step.subject ? 'text-muted-foreground' : 'text-foreground',
              )}
            >
              {subjectLine}
            </span>
            {sameThread && (
              <span className="shrink-0 rounded border border-border bg-surface-2 px-1 py-px font-mono text-[9px] uppercase tracking-[0.12em] text-faint">
                Same thread
              </span>
            )}
          </span>
          {bodyPreview && <span className="w-full truncate text-[12px] text-muted-foreground">{bodyPreview}</span>}
        </button>

        <div className="nodrag flex items-center gap-0.5 border-t border-border px-1.5 py-1">
          <Button
            variant="ghost"
            size="icon-sm"
            className="size-7"
            aria-label={`A/B variants for step ${position}`}
            onClick={() => actions.openVariants(step, position)}
          >
            <FlaskConical className="size-3.5" />
          </Button>
          {canModifyStructure && (
            <>
              <Button
                variant="ghost"
                size="icon-sm"
                className="size-7"
                aria-label={`Move step ${position} up`}
                disabled={position === 1 || actions.isReordering}
                onClick={() => actions.moveStep(step.id, -1)}
              >
                <ArrowUp className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                className="size-7"
                aria-label={`Move step ${position} down`}
                disabled={position === stepCount || actions.isReordering}
                onClick={() => actions.moveStep(step.id, 1)}
              >
                <ArrowDown className="size-3.5" />
              </Button>
            </>
          )}
          <span className="ml-auto" />
          {canModifyStructure ? (
            <Button
              variant="ghost"
              size="icon-sm"
              className="size-7 text-muted-foreground hover:text-danger"
              aria-label={`Delete step ${position}`}
              onClick={() => actions.requestDelete(step)}
            >
              <Trash2 className="size-3.5" />
            </Button>
          ) : (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="size-7"
                      aria-label={`Delete step ${position} (disabled — ${DRAFT_ONLY_HINT})`}
                      disabled
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </span>
                </TooltipTrigger>
                <TooltipContent>{DRAFT_ONLY_HINT}</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )}
        </div>
      </div>
    </FlowNodeFrame>
  )
}

/**
 * Reads the same per-step variants list the A/B dialog does, so opening the
 * dialog is a cache hit and adding a variant there updates the count here.
 * Says nothing while loading or on failure: the count is a hint, and the
 * dialog is where a variants error is actually surfaced and actionable.
 */
function VariantCount({ campaignId, stepId }: { campaignId: string; stepId: string }) {
  const { data } = useListStepVariantsQuery({ id: campaignId, stepId })
  const count = data?.length ?? 0
  if (count === 0) return null
  return (
    <span className="ml-auto flex items-center gap-1 rounded border border-border bg-surface-2 px-1.5 py-px font-mono text-[9.5px] uppercase tracking-[0.1em] text-muted-foreground">
      <FlaskConical className="size-2.5" aria-hidden="true" />
      {count} {count === 1 ? 'variant' : 'variants'}
    </span>
  )
}
