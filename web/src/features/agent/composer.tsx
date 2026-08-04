import { useEffect, useRef, useState } from 'react'
import { ArrowUp, Square, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import {
  clearRestoredAgentDraft,
  setAgentStreamError,
  setAgentStreamStatus,
  setSubmittedAgentDraft,
} from '@/store/slices/agent'
import {
  useCreateAgentThreadMutation,
  useDeleteAgentQueuedMessageMutation,
  useListAiModelsQuery,
  useSendAgentMessageMutation,
  useStopAgentRunMutation,
  type AgentBrowsingContext,
} from './api'

const DEFAULT_MODEL = 'default-smart-model'

function browsingContext(): AgentBrowsingContext | undefined {
  const { pathname, href, searchParams } = new URL(window.location.href)
  const campaign = pathname.match(/^\/app\/campaigns\/([0-9a-f-]{36})$/i)
  if (campaign) {
    return { type: 'record_page', object: 'campaign', record_id: campaign[1], url: href }
  }
  if (pathname === '/app/contacts') {
    const filters: Record<string, string> = {}
    searchParams.forEach((value, key) => {
      filters[key] = value
    })
    return { type: 'list_view', view: 'contacts', filters }
  }
  if (pathname === '/app/mailboxes') return { type: 'list_view', view: 'mailboxes' }
  if (pathname === '/app/campaigns') return { type: 'list_view', view: 'campaigns' }
  return undefined
}

export function AgentComposer({
  threadId,
  activeRunId,
  onSelectThread,
  suggestedPrompt,
}: {
  threadId: string | null
  activeRunId: string | null
  onSelectThread: (id: string) => void
  suggestedPrompt?: { id: number; text: string } | null
}) {
  const dispatch = useAppDispatch()
  const streamStatus = useAppSelector((state) => state.agent.streamStatus)
  const restoredDraft = useAppSelector((state) => state.agent.restoredDraft)
  const queue = useAppSelector((state) => state.agent.queued)
  const [draft, setDraft] = useState('')
  const [model, setModel] = useState(DEFAULT_MODEL)
  const [localError, setLocalError] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const modelsQuery = useListAiModelsQuery()
  const [createThread, createState] = useCreateAgentThreadMutation()
  const [sendMessage, sendState] = useSendAgentMessageMutation()
  const [stopRun, stopState] = useStopAgentRunMutation()
  const [deleteQueued] = useDeleteAgentQueuedMessageMutation()

  useEffect(() => {
    if (!restoredDraft) return
    setDraft((current) => current || restoredDraft)
    dispatch(clearRestoredAgentDraft())
    textareaRef.current?.focus()
  }, [dispatch, restoredDraft])

  useEffect(() => {
    if (!suggestedPrompt) return
    setDraft(suggestedPrompt.text)
    textareaRef.current?.focus()
  }, [suggestedPrompt])

  const isRunning = Boolean(activeRunId) || streamStatus === 'running'
  const isSending = createState.isLoading || sendState.isLoading

  const send = async () => {
    const text = draft.trim()
    if (!text || isSending) return
    setLocalError('')
    try {
      let id = threadId
      if (!id) {
        const created = await createThread().unwrap()
        id = created.id
        onSelectThread(id)
      }
      const result = await sendMessage({
        id,
        agentSendRequest: {
          text,
          model,
          browsing_context: browsingContext(),
        },
      }).unwrap()
      setDraft('')
      if (result.run_id) {
        dispatch(setSubmittedAgentDraft(text))
        dispatch(setAgentStreamStatus('running'))
      }
    } catch {
      setLocalError('Message could not be sent. Your draft has been kept.')
      dispatch(setAgentStreamError('Message could not be sent. Try again.'))
    }
  }

  return (
    <div className="shrink-0 border-t border-border bg-surface p-2.5">
      {queue.length > 0 && threadId && (
        <div className="mb-2 flex max-h-20 flex-col gap-1 overflow-y-auto">
          {queue.map((item, index) => (
            <div key={item.id} className="flex items-center gap-2 rounded-md bg-surface-2 px-2 py-1.5">
              <span className="font-mono text-[9px] text-faint">{index + 1}</span>
              <span className="min-w-0 flex-1 truncate text-[10px] text-muted-foreground">{item.text}</span>
              <button
                type="button"
                className="text-faint hover:text-danger"
                aria-label="Remove queued message"
                onClick={() => void deleteQueued({ id: threadId, messageId: item.id })}
              >
                <Trash2 className="size-3" />
              </button>
            </div>
          ))}
        </div>
      )}
      {localError && <p className="mb-1.5 text-[11px] text-danger">{localError}</p>}
      <div className="rounded-xl border border-border-strong bg-background p-1.5 shadow-[inset_0_1px_2px_var(--input-inset)] focus-within:border-primary">
        <Textarea
          ref={textareaRef}
          value={draft}
          rows={2}
          maxLength={20_000}
          placeholder="Ask Inroad anything..."
          className="min-h-14 resize-none border-0 bg-transparent px-2 py-1.5 shadow-none focus-visible:ring-0"
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              void send()
            }
          }}
        />
        <div className="flex items-center gap-2">
          <Select
            value={model}
            onChange={(event) => setModel(event.target.value)}
            wrapperClassName="min-w-0 flex-1"
            className="h-7 border-0 bg-transparent py-0 pl-2 text-[10px] shadow-none"
            aria-label="Agent model"
          >
            <option value={DEFAULT_MODEL}>Auto - recommended</option>
            {(modelsQuery.data?.models ?? [])
              .filter((item) => item.enabled)
              .map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label}
                </option>
              ))}
          </Select>
          {isRunning && threadId ? (
            <Button
              size="icon-sm"
              variant="secondary"
              aria-label="Stop agent"
              disabled={stopState.isLoading}
              onClick={() => void stopRun({ id: threadId })}
            >
              <Square className="size-3.5 fill-current" />
            </Button>
          ) : (
            <Button
              size="icon-sm"
              variant="primary"
              aria-label="Send message"
              disabled={!draft.trim() || isSending}
              onClick={() => void send()}
            >
              <ArrowUp className="size-4" />
            </Button>
          )}
        </div>
      </div>
      <p className="mt-1.5 text-center font-mono text-[8px] uppercase tracking-[0.12em] text-faint">
        Enter to send / Shift+Enter for a new line
      </p>
    </div>
  )
}
