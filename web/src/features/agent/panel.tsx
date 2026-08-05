import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
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
import { AgentAlert } from './alert'
import { AgentComposer } from './composer'
import { AgentHistory } from './history'
import { AgentMessageBubble } from './message'
import { useGetAgentThreadQuery, useListAgentApprovalsQuery, useListAgentQueueQuery, type AgentApproval } from './api'
import { useAgentStream } from './use-agent-stream'

const suggestions = [
  'Summarize campaign performance and flag what needs attention.',
  'Find contacts that need a follow-up and suggest the next step.',
  'Check mailbox health before I launch my next campaign.',
] as const

const minPanelWidth = 340
const maxPanelWidth = 640
const widthStep = 24
/** Distance from the bottom that still counts as "following along". */
const stickToBottomSlack = 96

interface SuggestedPrompt {
  id: number
  text: string
}

const MessageRecord = memo(function MessageRecord({ id, approvalsByCall }: { id: string; approvalsByCall: ReadonlyMap<string, AgentApproval> }) {
  const message = useAppSelector((state) => state.agent.messagesById[id])
  return message ? <AgentMessageBubble message={message} approvalsByCall={approvalsByCall} /> : null
})

function MessageScroller({
  loading,
  approvalsByCall,
  scrollRef,
}: {
  loading: boolean
  approvalsByCall: ReadonlyMap<string, AgentApproval>
  scrollRef: React.RefObject<HTMLDivElement | null>
}) {
  const ids = useAppSelector((state) => state.agent.messageIds)
  const streaming = useAppSelector((state) => state.agent.streaming)
  const endRef = useRef<HTMLDivElement>(null)

  // Only follow the stream while the reader is already at the bottom. Scrolling
  // up to re-read an earlier answer must not be yanked back on the next
  // 100 ms snapshot.
  useEffect(() => {
    const container = scrollRef.current
    if (!container) return
    const distance = container.scrollHeight - container.scrollTop - container.clientHeight
    if (distance > stickToBottomSlack) return
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [ids.length, streaming, scrollRef])

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
    // aria-live is deliberately off: the last bubble's text mutates every
    // 100 ms, and a polite region over it makes a screen reader re-announce
    // the whole growing answer. Transitions are announced once, separately,
    // by StreamAnnouncer.
    <div className="space-y-4 px-4 py-5" aria-live="off">
      {ids.map((id) => <MessageRecord key={id} id={id} approvalsByCall={approvalsByCall} />)}
      {streaming && <AgentMessageBubble message={streaming} streaming approvalsByCall={approvalsByCall} />}
      <div ref={endRef} aria-hidden="true" />
    </div>
  )
}

/** The one polite region: announces that a response started or finished, not its contents. */
function StreamAnnouncer() {
  const status = useAppSelector((state) => state.agent.streamStatus)
  const [announcement, setAnnouncement] = useState('')
  const previousRef = useRef(status)

  useEffect(() => {
    const previous = previousRef.current
    previousRef.current = status
    if (status === previous) return
    if (status === 'running') setAnnouncement('Assistant is responding')
    else if (previous === 'running' && status === 'idle') setAnnouncement('Response complete')
  }, [status])

  return <p className="sr-only" role="status" aria-live="polite">{announcement}</p>
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

const focusableSelector =
  'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])'

export function AgentPanel() {
  useAgentStream()
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const search = useSearch({ strict: false }) as { thread?: string }
  const deepLinkedThread = search.thread ?? null
  const open = useAppSelector((state) => state.ui.agentPanelOpen)
  const width = useAppSelector((state) => state.ui.agentPanelWidth)
  const page = useAppSelector((state) => state.ui.agentPanelPage)
  const threadId = useAppSelector((state) => state.agent.activeThreadId)
  const messageCount = useAppSelector((state) => state.agent.messageIds.length)
  const streamError = useAppSelector((state) => state.agent.streamError)
  const panelRef = useRef<HTMLElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const resizeRef = useRef({ startX: 0, startWidth: width, nextWidth: width })
  const promptCounterRef = useRef(0)
  const wasOpenRef = useRef(open)
  const [suggestedPrompt, setSuggestedPrompt] = useState<SuggestedPrompt | null>(null)

  const threadQuery = useGetAgentThreadQuery(
    { id: threadId ?? '' },
    { skip: !threadId },
  )
  const queueQuery = useListAgentQueueQuery(
    { id: threadId ?? '' },
    { skip: !threadId },
  )
  // Two reads, deliberately. The endpoint has no `thread_id` filter and caps at
  // 100, so an unfiltered page can push a live pending approval off the end and
  // freeze the run with no explanation; `status: 'pending'` guarantees the
  // blocking ones are present. The unfiltered page is what keeps a
  // just-decided card showing its outcome in the transcript. A `thread_id`
  // parameter would collapse this to one call — flagged as a backend follow-up.
  const pendingApprovals = useListAgentApprovalsQuery({ status: 'pending', limit: 100 }, { skip: !threadId })
  const recentApprovals = useListAgentApprovalsQuery({ limit: 100 }, { skip: !threadId })
  const approvalsByCall = useMemo(() => {
    const byCall = new Map<string, AgentApproval>()
    for (const action of recentApprovals.data?.actions ?? []) {
      if (action.thread_id === threadId) byCall.set(action.tool_call_id, action)
    }
    for (const action of pendingApprovals.data?.actions ?? []) {
      if (action.thread_id === threadId) byCall.set(action.tool_call_id, action)
    }
    return byCall
  }, [pendingApprovals.data?.actions, recentApprovals.data?.actions, threadId])

  const writeThreadToURL = useCallback(
    (next: string | null) => {
      // Pinned to the current pathname: the panel lives in the /app layout, so
      // a relative `to` would resolve to the layout and navigate the user off
      // whatever page they were reading.
      void navigate({
        to: pathname,
        search: (previous) => {
          const { thread: _dropped, ...rest } = previous as Record<string, unknown>
          return next ? { ...rest, thread: next } : rest
        },
      })
    },
    [navigate, pathname],
  )

  // The URL is the source of truth for which thread is shown, so back/forward
  // and a deep link all take the same path through the router instead of a
  // hand-rolled popstate listener the router doesn't know about.
  useEffect(() => {
    if (deepLinkedThread) {
      dispatch(selectAgentThread(deepLinkedThread))
      dispatch(setAgentPanelOpen(true))
    } else {
      dispatch(selectAgentThread(null))
    }
  }, [deepLinkedThread, dispatch])

  // Only on an open transition. The panel mounts already-open when the state
  // was persisted, and stealing focus onto Close after a reload is hostile.
  useEffect(() => {
    if (open && !wasOpenRef.current) closeButtonRef.current?.focus()
    wasOpenRef.current = open
  }, [open])

  // Mobile renders the panel as a modal overlay over the app; without a trap,
  // Tab walks into the page behind it.
  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return
      const panel = panelRef.current
      if (!panel || !window.matchMedia('(max-width: 639px)').matches) return
      const focusable = [...panel.querySelectorAll<HTMLElement>(focusableSelector)]
      const first = focusable[0]
      const last = focusable.at(-1)
      if (!first || !last) return
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
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
  }, [dispatch, writeThreadToURL])

  const newConversation = useCallback(() => {
    dispatch(selectAgentThread(null))
    dispatch(setAgentPanelPage('chat'))
    writeThreadToURL(null)
  }, [dispatch, writeThreadToURL])

  const requestPrompt = (text: string) => {
    promptCounterRef.current += 1
    setSuggestedPrompt({ id: promptCounterRef.current, text })
  }

  const applyWidth = (next: number) => {
    const clamped = Math.max(minPanelWidth, Math.min(maxPanelWidth, next))
    panelRef.current?.style.setProperty('--agent-panel-width', `${clamped}px`)
    dispatch(setAgentPanelWidth(clamped))
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
          tabIndex={0}
          aria-label="Resize assistant panel"
          aria-orientation="vertical"
          aria-valuenow={width}
          aria-valuemin={minPanelWidth}
          aria-valuemax={maxPanelWidth}
          aria-valuetext={`${width} pixels wide`}
          className="absolute inset-y-0 -left-1 hidden w-2 cursor-col-resize touch-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:block"
          onKeyDown={(event) => {
            const delta =
              event.key === 'ArrowLeft' ? widthStep : event.key === 'ArrowRight' ? -widthStep : 0
            if (delta !== 0) {
              event.preventDefault()
              applyWidth(width + delta)
              return
            }
            if (event.key === 'Home') {
              event.preventDefault()
              applyWidth(minPanelWidth)
            } else if (event.key === 'End') {
              event.preventDefault()
              applyWidth(maxPanelWidth)
            }
          }}
          onPointerDown={(event) => {
            resizeRef.current = { startX: event.clientX, startWidth: width, nextWidth: width }
            event.currentTarget.setPointerCapture(event.pointerId)
          }}
          onPointerMove={(event) => {
            if (!event.currentTarget.hasPointerCapture(event.pointerId)) return
            const nextWidth = Math.max(
              minPanelWidth,
              Math.min(maxPanelWidth, resizeRef.current.startWidth + resizeRef.current.startX - event.clientX),
            )
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
            <StreamAnnouncer />
            <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
              {!threadId && messageCount === 0 ? (
                <EmptyConversation onPrompt={requestPrompt} />
              ) : (
                <MessageScroller
                  loading={threadQuery.isLoading}
                  approvalsByCall={approvalsByCall}
                  scrollRef={scrollRef}
                />
              )}
            </div>
            {streamError && (
              <AgentAlert
                message={streamError}
                onDismiss={() => dispatch(setAgentStreamStatus('idle'))}
              />
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
