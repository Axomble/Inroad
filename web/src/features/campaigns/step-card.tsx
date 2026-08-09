import { FlaskConical, Pencil, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { SequenceStep } from './api'
import { humanizeDelay } from './step-delay'

/** A step guaranteed to carry an id — every persisted step has one. */
export type StepWithId = SequenceStep & { id: string }

const DRAFT_ONLY_HINT = 'Structural changes are draft-only'

/**
 * Presentational sequence-step card. The drag handle is injected as a slot so
 * the same body renders both inside a sortable wrapper (draft, from the
 * lazy-loaded `sortable-step-list`) and statically (non-draft), without
 * conditionally calling the `useSortable` hook. Exported so the code-split
 * sortable wrapper can reuse it without pulling `@dnd-kit` into this chunk.
 */
export function StepCardBody({
  step,
  position,
  threadSubject,
  canModifyStructure,
  onEdit,
  onDelete,
  onVariants,
  dragHandle,
  className,
}: {
  step: StepWithId
  /** 1-based display order. */
  position: number
  /**
   * The first step's subject. A follow-up with an empty subject sends inside
   * the same thread as "Re: <this>", so the card shows that real subject line
   * instead of a "(no subject)" that reads like an authoring mistake.
   */
  threadSubject?: string
  canModifyStructure: boolean
  onEdit: () => void
  onDelete: () => void
  /**
   * Opens the step's A/B variants. Available while RUNNING, unlike delete:
   * adding or reweighting an arm changes what future sends contain, which is the
   * same class of change as editing the body, not a structural edit.
   */
  onVariants: () => void
  dragHandle?: React.ReactNode
  className?: string
}) {
  const bodyPreview = step.body_text?.trim().replace(/\s+/g, ' ') ?? ''
  const sameThread = !step.subject && position > 1
  return (
    <li
      className={cn(
        'flex items-start gap-3 border-b border-border bg-surface/40 px-5 py-3.5 last:border-b-0',
        className,
      )}
    >
      {dragHandle}

      <div className="mt-0.5 shrink-0">
        <span className="inline-flex h-6 min-w-6 items-center justify-center rounded-md border border-border-strong bg-surface-2 px-1.5 font-mono text-[11px] tabular-nums text-muted-foreground">
          {position}
        </span>
      </div>

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">Step {position}</span>
          <span className="text-faint">·</span>
          <span className="text-[11.5px] text-muted-foreground">{humanizeDelay(step.delay_seconds)}</span>
        </div>
        {sameThread ? (
          <div className="mt-1 flex min-w-0 items-center gap-2">
            <span className="truncate text-[13.5px] font-medium text-muted-foreground">
              Re: {threadSubject || 'the previous email'}
            </span>
            <span className="shrink-0 rounded border border-border bg-surface-2 px-1.5 py-px font-mono text-[9.5px] uppercase tracking-[0.12em] text-faint">
              Same thread
            </span>
          </div>
        ) : (
          <div className="mt-1 truncate text-[13.5px] font-medium text-foreground">
            {step.subject || 'No subject yet'}
          </div>
        )}
        {bodyPreview && <div className="mt-0.5 truncate text-[12px] text-muted-foreground">{bodyPreview}</div>}
      </div>

      <div className="flex shrink-0 items-center gap-1">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`A/B variants for step ${position}`}
          onClick={onVariants}
        >
          <FlaskConical className="size-4" />
        </Button>
        <Button variant="ghost" size="icon-sm" aria-label={`Edit step ${position}`} onClick={onEdit}>
          <Pencil className="size-4" />
        </Button>
        {canModifyStructure ? (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Delete step ${position}`}
            className="text-muted-foreground hover:text-danger"
            onClick={onDelete}
          >
            <Trash2 className="size-4" />
          </Button>
        ) : (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Delete step ${position} (disabled — ${DRAFT_ONLY_HINT})`}
                    disabled
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </span>
              </TooltipTrigger>
              <TooltipContent>{DRAFT_ONLY_HINT}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </div>
    </li>
  )
}

/**
 * Static (non-draft) card: no drag handle, delete disabled with a tooltip,
 * content edit stays enabled.
 */
export function StepCard(props: {
  step: StepWithId
  position: number
  threadSubject?: string
  onEdit: () => void
  onDelete: () => void
  onVariants: () => void
}) {
  return <StepCardBody {...props} canModifyStructure={false} />
}
