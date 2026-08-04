import { useEffect, useState } from 'react'
import { Archive, Check, MessageSquarePlus, Pencil, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import {
  useDeleteAgentThreadMutation,
  useListAgentThreadsQuery,
  useRenameAgentThreadMutation,
  type AgentThread,
} from './api'

function dateGroup(value: string): string {
  const date = new Date(value)
  const today = new Date()
  const startToday = new Date(today.getFullYear(), today.getMonth(), today.getDate())
  const startDate = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const days = Math.round((startToday.getTime() - startDate.getTime()) / 86_400_000)
  if (days === 0) return 'Today'
  if (days === 1) return 'Yesterday'
  if (days < 7) return 'This week'
  return date.toLocaleDateString([], { month: 'long', year: 'numeric' })
}

function grouped(threads: AgentThread[]): Array<[string, AgentThread[]]> {
  const groups = new Map<string, AgentThread[]>()
  for (const thread of threads) {
    const key = dateGroup(thread.updated_at)
    groups.set(key, [...(groups.get(key) ?? []), thread])
  }
  return [...groups.entries()]
}

function HistoryRow({
  thread,
  active,
  onSelect,
  onArchived,
}: {
  thread: AgentThread
  active: boolean
  onSelect: () => void
  onArchived: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [title, setTitle] = useState(thread.title || 'New conversation')
  const [rename] = useRenameAgentThreadMutation()
  const [remove] = useDeleteAgentThreadMutation()

  useEffect(() => {
    if (!editing) setTitle(thread.title || 'New conversation')
  }, [editing, thread.title])

  if (editing) {
    return (
      <div className="flex items-center gap-1.5 rounded-lg bg-surface-2 p-1.5">
        <Input
          autoFocus
          value={title}
          maxLength={120}
          className="h-7 min-w-0 flex-1 text-[11px]"
          onChange={(event) => setTitle(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Escape') setEditing(false)
            if (event.key === 'Enter' && title.trim()) {
              void rename({ id: thread.id, body: { title: title.trim() } })
              setEditing(false)
            }
          }}
        />
        <button
          type="button"
          aria-label="Save title"
          onClick={() => {
            if (title.trim()) void rename({ id: thread.id, body: { title: title.trim() } })
            setEditing(false)
          }}
        >
          <Check className="size-3.5 text-ok" />
        </button>
        <button type="button" aria-label="Cancel rename" onClick={() => setEditing(false)}>
          <X className="size-3.5 text-faint" />
        </button>
      </div>
    )
  }

  return (
    <div className={cn('group flex items-center rounded-lg', active && 'bg-surface-2')}>
      <button type="button" onClick={onSelect} className="min-w-0 flex-1 px-2.5 py-2 text-left">
        <span className="block truncate text-[12px] font-medium text-foreground">
          {thread.title || 'New conversation'}
        </span>
        <span className="mt-0.5 block text-[9px] text-faint">
          {new Date(thread.updated_at).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}
        </span>
      </button>
      <div className="mr-1 flex opacity-0 group-hover:opacity-100 group-focus-within:opacity-100">
        <button type="button" className="p-1 text-faint hover:text-foreground" aria-label="Rename thread" onClick={() => setEditing(true)}>
          <Pencil className="size-3" />
        </button>
        <button
          type="button"
          className="p-1 text-faint hover:text-danger"
          aria-label="Archive thread"
          onClick={() => {
            if (window.confirm('Archive this conversation?')) {
              void remove({ id: thread.id }).unwrap().then(onArchived)
            }
          }}
        >
          <Archive className="size-3" />
        </button>
      </div>
    </div>
  )
}

export function AgentHistory({
  activeThreadId,
  onSelect,
  onNew,
}: {
  activeThreadId: string | null
  onSelect: (id: string) => void
  onNew: () => void
}) {
  const query = useListAgentThreadsQuery({ offset: 0, limit: 100 })
  const threads = query.data?.threads ?? []
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="p-3">
        <Button variant="primary" size="sm" className="w-full" onClick={onNew}>
          <MessageSquarePlus className="size-4" />
          New conversation
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {query.isLoading ? (
          <div className="space-y-2 px-1">
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </div>
        ) : threads.length === 0 ? (
          <p className="px-3 py-8 text-center text-[12px] text-muted-foreground">No conversations yet.</p>
        ) : (
          grouped(threads).map(([label, rows]) => (
            <section key={label} className="mb-4">
              <h3 className="px-2.5 pb-1 font-mono text-[9px] uppercase tracking-[0.16em] text-faint">{label}</h3>
              <div className="space-y-0.5">
                {rows.map((thread) => (
                  <HistoryRow
                    key={thread.id}
                    thread={thread}
                    active={thread.id === activeThreadId}
                    onSelect={() => onSelect(thread.id)}
                    onArchived={() => {
                      if (thread.id === activeThreadId) onNew()
                    }}
                  />
                ))}
              </div>
            </section>
          ))
        )}
      </div>
    </div>
  )
}
