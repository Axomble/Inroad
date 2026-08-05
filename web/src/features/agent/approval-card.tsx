import { memo, useEffect, useId, useMemo, useState } from 'react'
import { AlertTriangle, Check, Clock3, Loader2, Pencil, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import { useDecideAgentApprovalMutation, type AgentApproval } from './api'

function toolLabel(name: string): string {
  return name.replace(/^inroad_/, '').replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function timeRemaining(expiresAt: string, now: number): string {
  const milliseconds = new Date(expiresAt).getTime() - now
  if (milliseconds <= 0) return 'Expired'
  const minutes = Math.ceil(milliseconds / 60_000)
  if (minutes < 60) return `${minutes}m remaining`
  const hours = Math.ceil(minutes / 60)
  return `${hours}h remaining`
}

function isJSONObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export const ApprovalCard = memo(function ApprovalCard({
  action,
  compact = false,
}: {
  action: AgentApproval
  compact?: boolean
}) {
  const [mode, setMode] = useState<'preview' | 'edit' | 'reject'>('preview')
  const [editedText, setEditedText] = useState(() => JSON.stringify(action.edited_arguments ?? action.arguments, null, 2))
  const [reason, setReason] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const [now, setNow] = useState(Date.now())
  const [decide, decision] = useDecideAgentApprovalMutation()
  const editId = useId()
  const reasonId = useId()
  const pending = action.status === 'pending' && new Date(action.expires_at).getTime() > now

  useEffect(() => {
    if (action.status !== 'pending') return
    const timer = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(timer)
  }, [action.status])

  const argumentsText = useMemo(
    () => JSON.stringify(action.edited_arguments ?? action.arguments, null, 2),
    [action.arguments, action.edited_arguments],
  )

  const submit = async (kind: 'approve' | 'reject') => {
    setFormError(null)
    let editedArguments: Record<string, unknown> | undefined
    if (kind === 'approve' && mode === 'edit') {
      try {
        const parsed: unknown = JSON.parse(editedText)
        if (!isJSONObject(parsed)) throw new Error('not an object')
        editedArguments = parsed
      } catch {
        setFormError('Edited arguments must be a valid JSON object.')
        return
      }
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
    } catch {
      setFormError('This decision could not be saved. Refresh and try again; the action may have expired or already been decided.')
    }
  }

  const statusTone = action.status === 'executed' ? 'ok' : action.status === 'failed' || action.status === 'rejected' || action.status === 'expired' ? 'danger' : 'warm'

  return (
    <section className={cn('border border-warm/35 bg-warm/5', compact ? 'rounded-md p-2.5' : 'rounded-lg p-4')} aria-labelledby={`approval-${action.id}`}>
      <div className="flex flex-wrap items-start gap-2">
        <span className="grid size-8 shrink-0 place-items-center rounded-md bg-warm/15 text-warm"><AlertTriangle className="size-4" aria-hidden="true" /></span>
        <div className="min-w-0 flex-1">
          <h3 id={`approval-${action.id}`} className="text-sm font-semibold text-foreground">Approve {toolLabel(action.tool_name)}</h3>
          <p className="mt-0.5 text-xs leading-5 text-muted-foreground">The assistant is paused. Review the exact inputs before allowing this consequential action.</p>
        </div>
        <Badge variant={statusTone}>{action.status.replaceAll('_', ' ')}</Badge>
      </div>

      <div className="mt-3 flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <Clock3 className="size-3" aria-hidden="true" />
        <span>{timeRemaining(action.expires_at, now)}</span>
      </div>

      {mode === 'edit' ? (
        <div className="mt-3">
          <Label htmlFor={editId}>Edited action inputs (JSON)</Label>
          <Textarea id={editId} value={editedText} onChange={(event) => setEditedText(event.target.value)} className="mt-1 min-h-36 font-mono text-xs" aria-invalid={Boolean(formError)} />
          <p className="mt-1 text-[11px] text-muted-foreground">Only these edited inputs will be executed.</p>
        </div>
      ) : mode === 'reject' ? (
        <div className="mt-3">
          <Label htmlFor={reasonId}>Reason for rejecting</Label>
          <Textarea id={reasonId} value={reason} onChange={(event) => setReason(event.target.value)} maxLength={1000} className="mt-1 min-h-20" placeholder="Explain what should change before the assistant tries again." aria-invalid={Boolean(formError)} />
        </div>
      ) : (
        <details className="mt-3 rounded-md border border-border bg-background" open={!compact}>
          <summary className="cursor-pointer px-3 py-2 text-xs font-medium text-foreground">Action inputs</summary>
          <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words border-t border-border p-3 font-mono text-[11px] leading-5 text-muted-foreground">{argumentsText}</pre>
        </details>
      )}

      {formError && <p role="alert" className="mt-2 text-xs text-danger">{formError}</p>}
      {action.error && <p role="alert" className="mt-2 text-xs text-danger">{action.error}</p>}
      {action.decision_reason && <p className="mt-2 text-xs text-muted-foreground"><span className="font-medium text-foreground">Decision note:</span> {action.decision_reason}</p>}

      {pending && (
        <div className="mt-3 flex flex-wrap gap-2" aria-busy={decision.isLoading}>
          {mode === 'reject' ? (
            <Button size="sm" variant="destructive" disabled={decision.isLoading} onClick={() => void submit('reject')}>
              {decision.isLoading ? <Loader2 className="animate-spin" /> : <X />} Reject action
            </Button>
          ) : (
            <Button size="sm" variant="primary" disabled={decision.isLoading} onClick={() => void submit('approve')}>
              {decision.isLoading ? <Loader2 className="animate-spin" /> : <Check />} {mode === 'edit' ? 'Approve edited action' : 'Approve action'}
            </Button>
          )}
          <Button size="sm" variant="outline" disabled={decision.isLoading} onClick={() => { setFormError(null); setMode(mode === 'edit' ? 'preview' : 'edit') }}><Pencil />{mode === 'edit' ? 'Cancel edit' : 'Edit inputs'}</Button>
          <Button size="sm" variant="ghost" disabled={decision.isLoading} onClick={() => { setFormError(null); setMode(mode === 'reject' ? 'preview' : 'reject') }}>{mode === 'reject' ? 'Cancel' : 'Reject'}</Button>
        </div>
      )}
      <span className="sr-only" role="status">Approval status: {action.status}</span>
    </section>
  )
})
