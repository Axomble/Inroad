import { memo, useCallback, useEffect, useRef, useState } from 'react'
import { ArrowLeft, History, MessageSquarePlus, Sparkles, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import {
  replaceAgentMessages,
  selectAgentThread,
  setAgentQueue,
  setAgentStreamStatus,
} from '@/store/slices/agent'
import {
  setAgentPanelOpen,
  setAgentPanelPage,
  setAgentPanelWidth,
} from '@/store/slices/ui'
import { AgentComposer } from './composer'
import { AgentHistory } from './history'
import { AgentMessageBubble } from './message'
import { useGetAgentThreadQuery, useListAgentQueueQuery } from './api'
import { useAgentStream } from './use-agent-stream'

const suggestions = [
  'Summarize campaign performance and flag what needs attention.',
  'Find contacts that need a follow-up and suggest the next step.',
  'Check mailbox health before I launch my next campaign.',
] as const

interface SuggestedPrompt {
  id: number
  text: string
}

const MessageRecord = memo(function MessageRecord({ id }: { id: string }) {
  const message = useAppSelector((state) => state.agent.messagesById[id])
  return message ? <AgentMessageBubble message={message} /> : null
})

function MessageScroller({ loading }: { loading: boolean }) {
  const ids = useAppSelector((state) => state.agent.messageIds)
  const streaming = useAppSelector((state) => state.agent.streaming)
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [ids.length, streaming])

  if (loading && ids.length === 0) {
    return (
      <div className="space-y-5 p-4" aria-label="Loading conversation">
        <Skeleton className="ml-auto h-16 w-3/4 rounded-2xl" />
        <div className="space-y-2">
          <Skeleton className="h-3 w-5/6" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4 px-4 py-5" aria-live="polite" aria-relevant="additions text">
      {ids.map((id) => <MessageRecord key={id} id={id} />)}
      {streaming && <AgentMessageBubble message={streaming} streaming />}
      <div ref={endRef} aria-hidden="true" />
    </div>
  )
}

function EmptyConversation({ onPrompt }: { onPrompt: (text: string) => void }) {
  return (
    <div className="flex min-h-full flex-col items-center justify-center px-6 py-10 text-center">
      <div className="grid size-11 place-items-center rounded-2xl border border-primary/35 bg-primary/10 text-primary shadow-[0_0_28px_var(--primary-glow)]">
        <Sparkles className="size-5" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-[15px] font-semibold tracking-tight text-foreground">What can I help you move forward?</h2>
      <p className="mt-1.5 max-w-xs text-[11px] leading-5 text-muted-foreground">
        Ask about campaigns, contacts, mailbox health, or let the assistant take a safe action for you.
      </p>
      <div className="mt-5 grid w-full gap-2">
        {suggestions.map((text) => (
          <button
            key={text}
            type="button"
            onClick={() => onPrompt(text)}
            className="rounded-xl border border-border bg-surface px-3 py-2.5 text-left text-[11px] leading-4 text-muted-foreground transition-colors hover:border-border-strong hover:bg-surface-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {text}
          </button>
        ))}
      </div>
    </div>
  )
}

function threadFromURL(): string | null {
  return new URL(window.location.href).searchParams.get('thread')
}

function writeThreadToURL(threadId: string | null, replace = false): void {
  const url = new URL(window.location.href)
  if (threadId) url.searchParams.set('thread', threadId)
  else url.searchParams.delete('thread')
  window.history[replace ? 'replaceState' : 'pushState']({}, '', url)
}

export function AgentPanel() {
  useAgentStream()
  const dispatch = useAppDispatch()
  const open = useAppSelector((state) => state.ui.agentPanelOpen)
  const width = useAppSelector((state) => state.ui.agentPanelWidth)
  const page = useAppSelector((state) => state.ui.agentPanelPage)
  const threadId = useAppSelector((state) => state.agent.activeThreadId)
  const messageCount = useAppSelector((state) => state.agent.messageIds.length)
  const streamError = useAppSelector((state) => state.agent.streamError)
  const panelRef = useRef<HTMLElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const resizeRef = useRef({ startX: 0, startWidth: width, nextWidth: width })
  const promptCounterRef = useRef(0)
  const [suggestedPrompt, setSuggestedPrompt] = useState<SuggestedPrompt | null>(null)

  const threadQuery = useGetAgentThreadQuery(
    { id: threadId ?? '' },
    { skip: !threadId },
  )
  const queueQuery = useListAgentQueueQuery(
    { id: threadId ?? '' },
    { skip: !threadId },
  )

  useEffect(() => {
    const deepLinkedThread = threadFromURL()
    if (deepLinkedThread) {
      dispatch(selectAgentThread(deepLinkedThread))
      dispatch(setAgentPanelOpen(true))
    }
    const onPopState = () => {
      const nextThread = threadFromURL()
      dispatch(selectAgentThread(nextThread))
      if (nextThread) dispatch(setAgentPanelOpen(true))
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [dispatch])

  useEffect(() => {
    if (open) closeButtonRef.current?.focus()
  }, [open])

  useEffect(() => {
    if (threadQuery.data && threadQuery.data.id === threadId) {
      dispatch(replaceAgentMessages(threadQuery.data.messages ?? []))
    }
  }, [dispatch, threadId, threadQuery.data])

  useEffect(() => {
    if (queueQuery.data && threadId) dispatch(setAgentQueue(queueQuery.data.queued))
  }, [dispatch, queueQuery.data, threadId])

  const selectThread = useCallback((id: string) => {
    dispatch(selectAgentThread(id))
    dispatch(setAgentPanelPage('chat'))
    writeThreadToURL(id)
  }, [dispatch])

  const newConversation = useCallback(() => {
    dispatch(selectAgentThread(null))
    dispatch(setAgentPanelPage('chat'))
    writeThreadToURL(null)
  }, [dispatch])

  const requestPrompt = (text: string) => {
    promptCounterRef.current += 1
    setSuggestedPrompt({ id: promptCounterRef.current, text })
  }

  const panelStyle = {
    '--agent-panel-width': `${width}px`,
  } as React.CSSProperties & { '--agent-panel-width': string }

  return (
    <>
      <div
        aria-hidden="true"
        onClick={() => dispatch(setAgentPanelOpen(false))}
        className={cn(
          'fixed inset-0 z-40 bg-background/65 backdrop-blur-sm transition-opacity sm:hidden',
          open ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
      />
      <aside
        ref={panelRef}
        aria-label="Inroad assistant"
        aria-hidden={!open}
        inert={!open}
        style={panelStyle}
        className={cn(
          'fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-border-strong bg-background shadow-2xl transition-transform sm:static sm:z-auto sm:w-[var(--agent-panel-width)] sm:shrink-0 sm:shadow-none sm:transition-[width]',
          open ? 'translate-x-0' : 'translate-x-full sm:hidden',
        )}
      >
        <div
          role="separator"
          aria-label="Resize assistant panel"
          aria-orientation="vertical"
          className="absolute inset-y-0 -left-1 hidden w-2 cursor-col-resize touch-none sm:block"
          onPointerDown={(event) => {
            resizeRef.current = { startX: event.clientX, startWidth: width, nextWidth: width }
            event.currentTarget.setPointerCapture(event.pointerId)
          }}
          onPointerMove={(event) => {
            if (!event.currentTarget.hasPointerCapture(event.pointerId)) return
            const nextWidth = Math.max(340, Math.min(640, resizeRef.current.startWidth + resizeRef.current.startX - event.clientX))
            resizeRef.current.nextWidth = nextWidth
            panelRef.current?.style.setProperty('--agent-panel-width', `${nextWidth}px`)
          }}
          onPointerUp={(event) => {
            if (!event.currentTarget.hasPointerCapture(event.pointerId)) return
            event.currentTarget.releasePointerCapture(event.pointerId)
            dispatch(setAgentPanelWidth(resizeRef.current.nextWidth))
          }}
        />

        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-surface px-2.5">
          {page === 'history' && (
            <Button size="icon-sm" variant="ghost" aria-label="Back to conversation" onClick={() => dispatch(setAgentPanelPage('chat'))}>
              <ArrowLeft className="size-4" />
            </Button>
          )}
          <div className="min-w-0 flex-1">
            <p className="truncate text-[12px] font-semibold text-foreground">
              {page === 'history' ? 'Conversation history' : (threadQuery.data?.title || 'Inroad assistant')}
            </p>
            {page === 'chat' && (
              <p className="font-mono text-[8px] uppercase tracking-[0.14em] text-faint">
                {threadQuery.data?.active_run_id ? 'Working' : 'Ready'}
              </p>
            )}
          </div>
          {page === 'chat' && (
            <>
              <Button size="icon-sm" variant="ghost" aria-label="Conversation history" onClick={() => dispatch(setAgentPanelPage('history'))}>
                <History className="size-4" />
              </Button>
              <Button size="icon-sm" variant="ghost" aria-label="New conversation" onClick={newConversation}>
                <MessageSquarePlus className="size-4" />
              </Button>
            </>
          )}
          <Button ref={closeButtonRef} size="icon-sm" variant="ghost" aria-label="Close assistant" onClick={() => dispatch(setAgentPanelOpen(false))}>
            <X className="size-4" />
          </Button>
        </header>

        {page === 'history' ? (
          <AgentHistory activeThreadId={threadId} onSelect={selectThread} onNew={newConversation} />
        ) : (
          <>
            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
              {!threadId && messageCount === 0 ? (
                <EmptyConversation onPrompt={requestPrompt} />
              ) : (
                <MessageScroller loading={threadQuery.isLoading} />
              )}
            </div>
            {streamError && (
              <div role="alert" className="flex items-center gap-2 border-t border-danger/25 bg-danger/10 px-3 py-2 text-[10px] text-danger">
                <span className="min-w-0 flex-1">{streamError}</span>
                <button type="button" className="font-semibold underline underline-offset-2" onClick={() => dispatch(setAgentStreamStatus('idle'))}>Dismiss</button>
              </div>
            )}
            <AgentComposer
              threadId={threadId}
              activeRunId={threadQuery.data?.active_run_id ?? null}
              onSelectThread={selectThread}
              suggestedPrompt={suggestedPrompt}
            />
          </>
        )}
      </aside>
    </>
  )
}
