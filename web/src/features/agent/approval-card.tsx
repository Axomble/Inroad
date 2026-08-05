import { memo, useEffect, useId, useMemo, useState } from 'react'
import { AlertTriangle, Braces, Check, Clock3, Loader2, Pencil, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import { useDecideAgentApprovalMutation, type AgentApproval } from './api'
import {
  createDraft,
  diffArguments,
  draftArguments,
  hasRenderedView,
  isJSONObject,
  type EditDraft,
  type ToolArguments,
} from './approval-args'
import { ActionSummary, ApprovalDiff, ApprovalEditor, ApprovalPreview } from './approval-preview'
import { approvalDecisionMessage } from './error-copy'

/** Under this much time left the countdown ticks by the second, so "expired" never lags. */
const secondPrecisionWindow = 120_000

function toolLabel(name: string): string {
  return name.replace(/^inroad_/, '').replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function timeRemaining(milliseconds: number): string {
  if (milliseconds <= 0) return 'Expired'
  const seconds = Math.ceil(milliseconds / 1000)
  if (seconds < 60) return `${seconds}s remaining`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s remaining`
  return `${Math.ceil(minutes / 60)}h remaining`
}

export const ApprovalCard = memo(function ApprovalCard({
  action,
  compact = false,
}: {
  action: AgentApproval
  compact?: boolean
}) {
  const [mode, setMode] = useState<'preview' | 'edit' | 'reject'>('preview')
  const [reason, setReason] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const [decide, decision] = useDecideAgentApprovalMutation()
  const reasonId = useId()
  const editId = useId()

  const currentArguments: ToolArguments = useMemo(() => {
    const value = action.edited_arguments ?? action.arguments
    return isJSONObject(value) ? value : {}
  }, [action.arguments, action.edited_arguments])

  const [draft, setDraft] = useState<EditDraft>(() => createDraft(action.tool_name, currentArguments))

  const expiresAt = useMemo(() => new Date(action.expires_at).getTime(), [action.expires_at])
  const remaining = expiresAt - now
  const expired = remaining <= 0
  const pending = action.status === 'pending' && !expired

  // Tick by the second in the last two minutes, by the half-minute before
  // that, and stop entirely once expired — a card that keeps a live interval
  // after its deadline is a leak on a page that renders up to a hundred. The
  // interval only re-arms when the cadence itself changes, not on every tick.
  const cadence = remaining <= secondPrecisionWindow ? 1000 : 30_000
  useEffect(() => {
    if (action.status !== 'pending' || expired) return
    const timer = window.setInterval(() => setNow(Date.now()), cadence)
    return () => window.clearInterval(timer)
  }, [action.status, cadence, expired])

  const proposedArguments: ToolArguments = useMemo(
    () => (isJSONObject(action.arguments) ? action.arguments : {}),
    [action.arguments],
  )
  const draftResult = useMemo(() => draftArguments(draft), [draft])
  const changes = useMemo(
    () => (draftResult.ok ? diffArguments(proposedArguments, draftResult.value) : []),
    [draftResult, proposedArguments],
  )

  const toggleJSONMode = () => {
    setFormError(null)
    if (draft.tool === 'json') {
      const parsed = draftArguments(draft)
      if (!parsed.ok) {
        setFormError(parsed.message)
        return
      }
      setDraft(createDraft(action.tool_name, parsed.value))
      return
    }
    const current = draftArguments(draft)
    setDraft({ tool: 'json', text: JSON.stringify(current.ok ? current.value : currentArguments, null, 2) })
  }

  const startEdit = () => {
    setFormError(null)
    setDraft(createDraft(action.tool_name, currentArguments))
    setMode('edit')
  }

  const submit = async (kind: 'approve' | 'reject') => {
    setFormError(null)
    let editedArguments: ToolArguments | undefined
    if (kind === 'approve' && mode === 'edit') {
      if (!draftResult.ok) {
        setFormError(draftResult.message)
        return
      }
      editedArguments = draftResult.value
    }
    if (kind === 'reject' && reason.trim().length === 0) {
      setFormError('Tell the assistant why this action should not run.')
      return
    }
    try {
      await decide({
        actionId: action.id,
        agentApprovalDecisionRequest: {
          decision: kind,
          ...(editedArguments ? { edited_arguments: editedArguments } : {}),
          ...(kind === 'reject' ? { reason: reason.trim() } : {}),
        },
      }).unwrap()
      setMode('preview')
    } catch (error) {
      setFormError(approvalDecisionMessage(error))
    }
  }

  const statusLabel = expired && action.status === 'pending' ? 'expired' : action.status
  const statusTone =
    action.status === 'executed'
      ? 'ok'
      : statusLabel === 'expired' || action.status === 'failed' || action.status === 'rejected'
        ? 'danger'
        : 'warm'

  return (
    <section
      className={cn('border border-warm/35 bg-warm/5', compact ? 'rounded-md p-2.5' : 'rounded-lg p-4')}
      aria-labelledby={`approval-${action.id}`}
    >
      <div className="flex flex-wrap items-start gap-2">
        <span className="grid size-8 shrink-0 place-items-center rounded-md bg-warm/15 text-warm">
          <AlertTriangle className="size-4" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1">
          <h3 id={`approval-${action.id}`} className="text-sm font-semibold text-foreground">
            Approve {toolLabel(action.tool_name)}
          </h3>
          <div className="mt-0.5">
            <ActionSummary toolName={action.tool_name} args={currentArguments} />
          </div>
        </div>
        <Badge variant={statusTone}>{statusLabel.replaceAll('_', ' ')}</Badge>
      </div>

      {action.status === 'pending' && (
        <div
          className={cn(
            'mt-3 flex items-center gap-1.5 text-[11px]',
            expired ? 'text-danger' : 'text-muted-foreground',
          )}
        >
          <Clock3 className="size-3" aria-hidden="true" />
          <span>{timeRemaining(remaining)}</span>
        </div>
      )}

      {mode === 'edit' ? (
        <div className="mt-3 space-y-3">
          <ApprovalEditor
            draft={draft}
            onChange={setDraft}
            invalid={Boolean(formError)}
            idPrefix={editId}
          />
          <ApprovalDiff changes={changes} />
          <p className="text-[11px] text-muted-foreground">Only these edited inputs will be executed.</p>
        </div>
      ) : mode === 'reject' ? (
        <div className="mt-3">
          <Label htmlFor={reasonId}>Reason for rejecting</Label>
          <Textarea
            id={reasonId}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            maxLength={1000}
            className="mt-1 min-h-20"
            placeholder="Explain what should change before the assistant tries again."
            aria-invalid={Boolean(formError)}
          />
        </div>
      ) : (
        <details className="mt-3" open={!compact}>
          <summary className="cursor-pointer py-1 text-xs font-medium text-foreground">Action inputs</summary>
          <div className="mt-2">
            <ApprovalPreview toolName={action.tool_name} args={currentArguments} />
          </div>
        </details>
      )}

      {formError && <p role="alert" className="mt-2 text-xs text-danger">{formError}</p>}
      {action.error && <p role="alert" className="mt-2 text-xs text-danger">{action.error}</p>}
      {action.decision_reason && (
        <p className="mt-2 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">Decision note:</span> {action.decision_reason}
        </p>
      )}

      {pending ? (
        <div className="mt-3 flex flex-wrap gap-2" aria-busy={decision.isLoading}>
          {mode === 'reject' ? (
            <Button size="sm" variant="destructive" disabled={decision.isLoading} onClick={() => void submit('reject')}>
              {decision.isLoading ? <Loader2 className="animate-spin" /> : <X />} Reject action
            </Button>
          ) : (
            <Button size="sm" variant="primary" disabled={decision.isLoading} onClick={() => void submit('approve')}>
              {decision.isLoading ? <Loader2 className="animate-spin" /> : <Check />}{' '}
              {mode === 'edit' ? 'Approve edited action' : 'Approve action'}
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            disabled={decision.isLoading}
            onClick={() => {
              setFormError(null)
              if (mode === 'edit') setMode('preview')
              else startEdit()
            }}
          >
            <Pencil />
            {mode === 'edit' ? 'Cancel edit' : 'Edit inputs'}
          </Button>
          {mode === 'edit' && hasRenderedView(action.tool_name) && (
            <Button size="sm" variant="ghost" disabled={decision.isLoading} onClick={toggleJSONMode}>
              <Braces />
              {draft.tool === 'json' ? 'Back to fields' : 'Edit as JSON'}
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={decision.isLoading}
            onClick={() => {
              setFormError(null)
              setMode(mode === 'reject' ? 'preview' : 'reject')
            }}
          >
            {mode === 'reject' ? 'Cancel' : 'Reject'}
          </Button>
        </div>
      ) : (
        action.status === 'pending' && (
          <p className="mt-3 text-xs text-danger">
            This action expired before it was reviewed, so the assistant did not run it.
          </p>
        )
      )}
      <span className="sr-only" role="status">
        Approval status: {statusLabel}
      </span>
    </section>
  )
})
