import { lazy, memo, Suspense, useEffect, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Copy, Info, Wrench } from 'lucide-react'
import { formatTime } from '@/lib/datetime'
import { cn } from '@/lib/utils'
import { Skeleton } from '@/components/ui/skeleton'
import type { AgentApproval, AgentMessage, AgentPart } from './api'
import { ApprovalCard } from './approval-card'
import type { StreamingMessage, StreamingPart } from '@/store/slices/agent'

const Markdown = lazy(() =>
  import('./markdown').then((module) => ({ default: module.AgentMarkdown })),
)

type PartView = AgentPart | StreamingPart
const emptyApprovals = new Map<string, AgentApproval>()

// Compaction notices are the system telling the user that earlier turns were
// summarised away. Folding them into the assistant's markdown makes the system
// sound like the assistant, so they render as their own chip.
function textOf(parts: PartView[]): string {
  return parts
    .filter((part) => part.type === 'text')
    .map((part) => part.text ?? '')
    .join('')
}

function CompactionNotices({ parts }: { parts: PartView[] }) {
  if (parts.length === 0) return null
  return (
    <div className="mb-2 space-y-1">
      {parts.map((part) => (
        <p
          key={part.id}
          className="flex items-start gap-1.5 rounded-md border border-border bg-surface-2 px-2 py-1.5 text-[10px] leading-4 text-muted-foreground"
        >
          <Info className="mt-px size-3 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{part.text}</span>
        </p>
      ))}
    </div>
  )
}

function toolLabel(name: string | undefined): string {
  return (name ?? 'Tool')
    .replace(/^inroad_/, '')
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function JsonView({ value }: { value: unknown }) {
  if (value === undefined) return <span className="text-faint">No data</span>
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  return (
    <pre className="max-h-52 overflow-auto whitespace-pre-wrap break-words rounded-md bg-background p-2 font-mono text-[10px] leading-4 text-muted-foreground">
      {text}
    </pre>
  )
}

function ToolRow({ part, approval }: { part: PartView; approval?: AgentApproval }) {
  const [tab, setTab] = useState<'input' | 'output'>('input')
  const running = part.state === 'running' || !part.state
  return (
    <div className="border-t border-border first:border-t-0">
      <div className="flex items-center gap-2 px-2.5 py-2">
        <span className={cn('grid size-5 place-items-center rounded-md bg-surface-2', running && 'agent-shimmer')}>
          {running ? <Wrench className="size-3" /> : <Check className="size-3 text-ok" />}
        </span>
        <span className={cn('min-w-0 flex-1 truncate text-[11px]', running ? 'text-foreground' : 'text-muted-foreground')}>
          {'loading_message' in part && part.loading_message
            ? part.loading_message
            : toolLabel(part.tool_name)}
        </span>
        <span className={cn('text-[9px] uppercase tracking-wider', part.state === 'error' ? 'text-danger' : 'text-faint')}>
          {running ? 'running' : part.state}
        </span>
      </div>
      {!running && (
        <div className="px-2.5 pb-2.5">
          <div className="mb-1.5 flex gap-1">
            {(['input', 'output'] as const).map((value) => (
              <button
                type="button"
                key={value}
                className={cn(
                  'rounded px-1.5 py-0.5 font-mono text-[9px] uppercase',
                  tab === value ? 'bg-surface-2 text-foreground' : 'text-faint',
                )}
                onClick={() => setTab(value)}
              >
                {value}
              </button>
            ))}
          </div>
          <JsonView value={tab === 'input' ? part.tool_input : part.tool_output} />
          {part.error && <p className="mt-1.5 text-[11px] text-danger">{part.error}</p>}
        </div>
      )}
      {approval && <div className="px-2.5 pb-2.5"><ApprovalCard action={approval} compact /></div>}
    </div>
  )
}

function ToolSteps({ parts, streaming, hasText, approvalsByCall }: { parts: PartView[]; streaming: boolean; hasText: boolean; approvalsByCall: ReadonlyMap<string, AgentApproval> }) {
  const [open, setOpen] = useState(streaming && !hasText)
  useEffect(() => {
    if (hasText) setOpen(false)
  }, [hasText])
  if (parts.length === 0) return null
  return (
    <div className="mb-3 overflow-hidden rounded-lg border border-border bg-surface">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 px-2.5 py-2 text-left text-[11px] font-medium text-muted-foreground hover:bg-surface-2"
        aria-expanded={open}
      >
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        {parts.length} {parts.length === 1 ? 'step' : 'steps'}
        {streaming && !hasText && <span className="ml-auto size-1.5 rounded-full bg-primary agent-pulse" />}
      </button>
      {open && <div>{parts.map((part) => <ToolRow key={part.id} part={part} approval={part.tool_call_id ? approvalsByCall.get(part.tool_call_id) : undefined} />)}</div>}
    </div>
  )
}

function Reasoning({ parts }: { parts: PartView[] }) {
  const text = parts.map((part) => part.reasoning ?? '').join('')
  if (!text) return null
  return (
    <details className="mb-2 text-[11px] text-muted-foreground">
      <summary className="cursor-pointer select-none font-medium">Reasoning</summary>
      <p className="mt-1 whitespace-pre-wrap border-l border-border pl-2 leading-5">{text}</p>
    </details>
  )
}

export const AgentMessageBubble = memo(function AgentMessageBubble({
  message,
  streaming = false,
  approvalsByCall = emptyApprovals,
}: {
  message: AgentMessage | StreamingMessage
  streaming?: boolean
  approvalsByCall?: ReadonlyMap<string, AgentApproval>
}) {
  const isUser = 'role' in message && message.role === 'user'
  const parts = message.parts as PartView[]
  const text = textOf(parts)
  const tools = parts.filter((part) => part.type === 'tool_call')
  const reasoning = parts.filter((part) => part.type === 'reasoning')
  const notices = parts.filter((part) => part.type === 'compaction_notice')
  const createdAt = 'created_at' in message ? message.created_at : message.createdAt
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')

  // The clipboard rejects on insecure origins and when permission is denied;
  // showing the copied check regardless is a lie the user acts on.
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopyState('copied')
      window.setTimeout(() => setCopyState('idle'), 1200)
    } catch {
      setCopyState('failed')
    }
  }

  return (
    <article className={cn('group flex w-full flex-col', isUser && 'items-end')}>
      <div
        className={cn(
          'min-w-0',
          isUser
            ? 'max-w-[86%] rounded-2xl rounded-br-md bg-surface-2 px-3 py-2'
            : 'w-full',
        )}
      >
        {!isUser && <CompactionNotices parts={notices} />}
        {!isUser && <ToolSteps parts={tools} streaming={streaming} hasText={Boolean(text)} approvalsByCall={approvalsByCall} />}
        {!isUser && <Reasoning parts={reasoning} />}
        {text && (
          <Suspense fallback={<div className="space-y-2"><Skeleton className="h-3 w-4/5" /><Skeleton className="h-3 w-2/3" /></div>}>
            <Markdown text={text} />
          </Suspense>
        )}
        {streaming && !text && tools.length === 0 && (
          <div className="flex items-center gap-1.5 py-2 text-[11px] text-muted-foreground">
            <span className="size-1.5 rounded-full bg-primary agent-pulse" />
            Thinking
          </div>
        )}
      </div>
      <footer className="mt-1 flex h-5 items-center gap-1.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
        <time className="text-[9px] text-faint">
          {createdAt ? formatTime(createdAt) : ''}
        </time>
        {text && (
          <>
            <button
              type="button"
              className="rounded p-0.5 text-faint hover:text-foreground"
              aria-label="Copy message"
              onClick={() => void copy()}
            >
              {copyState === 'copied' ? <Check className="size-3" /> : <Copy className="size-3" />}
            </button>
            {copyState === 'failed' && (
              <span role="alert" className="text-[9px] text-danger">
                Copy blocked by the browser
              </span>
            )}
          </>
        )}
      </footer>
    </article>
  )
})
