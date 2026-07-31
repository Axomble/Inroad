import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { LayoutDashboard, Mail, Megaphone, Users, Settings, Flame, CornerDownLeft } from 'lucide-react'
import { cn } from '@/lib/utils'

interface Command {
  id: string
  label: string
  /** The nav group this belongs to, shown as a trailing hint. */
  group: string
  icon: typeof Mail
  to: string
  /** Extra words that should match this command but aren't in the label. */
  keywords?: string
}

const COMMANDS: readonly Command[] = [
  { id: 'overview', label: 'Overview', group: 'Workspace', icon: LayoutDashboard, to: '/app', keywords: 'home dashboard health activity' },
  { id: 'mailboxes', label: 'Mailboxes', group: 'Sending', icon: Mail, to: '/app/mailboxes', keywords: 'smtp gmail microsoft connect accounts' },
  { id: 'warmup', label: 'Warmup', group: 'Sending', icon: Flame, to: '/app/warmup', keywords: 'reputation ramp health pool deliverability' },
  { id: 'campaigns', label: 'Campaigns', group: 'Outreach', icon: Megaphone, to: '/app/campaigns', keywords: 'sequence steps send launch' },
  { id: 'contacts', label: 'Contacts', group: 'Outreach', icon: Users, to: '/app/contacts', keywords: 'lists import csv leads' },
  { id: 'team', label: 'Team', group: 'Workspace', icon: Settings, to: '/app/settings/team', keywords: 'invites members roles settings' },
]

/**
 * ⌘K navigation.
 *
 * Deliberately built on the app's own primitives instead of adding a `cmdk`
 * dependency: the command set is five routes, and hand-rolling the list keeps the
 * lazy chunk tiny and the styling consistent with the rest of the menus.
 *
 * Rendered only while open (the parent lazy-loads this module on first ⌘K), so
 * the whole thing is absent from the initial bundle *and* from the DOM until it's
 * wanted.
 *
 * Keyboard model is self-contained rather than using `useHotkey`, because while
 * this is open it must own every key — the underlying list's `j`/`k` bindings
 * would otherwise fire behind the dialog. `useHotkey` already declines to fire
 * inside `[role="dialog"]`, which is what makes that separation work.
 */
export function CommandPalette({ onClose }: { onClose: () => void }) {
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return COMMANDS
    return COMMANDS.filter((c) =>
      `${c.label} ${c.group} ${c.keywords ?? ''}`.toLowerCase().includes(q),
    )
  }, [query])

  // A narrowed list must not leave the cursor pointing past the end.
  useEffect(() => {
    setActiveIndex(0)
  }, [query])

  const run = (command: Command | undefined) => {
    if (!command) return
    void navigate({ to: command.to })
    onClose()
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-background/70 p-4 pt-[12vh] backdrop-blur-sm"
      // A click on the scrim dismisses; clicks inside the panel must not.
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault()
            onClose()
          } else if (e.key === 'ArrowDown' || (e.key === 'j' && e.ctrlKey)) {
            e.preventDefault()
            setActiveIndex((i) => Math.min(i + 1, matches.length - 1))
          } else if (e.key === 'ArrowUp' || (e.key === 'k' && e.ctrlKey)) {
            e.preventDefault()
            setActiveIndex((i) => Math.max(i - 1, 0))
          } else if (e.key === 'Enter') {
            e.preventDefault()
            run(matches[activeIndex])
          }
        }}
        className="w-full max-w-lg overflow-hidden rounded-xl border border-border-strong bg-popover shadow-2xl"
      >
        <input
          ref={inputRef}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Go to…"
          aria-label="Search commands"
          className="h-12 w-full border-b border-border bg-transparent px-4 text-sm text-foreground outline-none placeholder:text-faint"
        />

        {matches.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-muted-foreground">
            Nothing matches “{query}”.
          </p>
        ) : (
          <ul className="max-h-80 overflow-y-auto p-1.5">
            {matches.map((command, index) => {
              const Icon = command.icon
              const isActive = index === activeIndex
              return (
                <li key={command.id}>
                  <button
                    type="button"
                    onMouseEnter={() => setActiveIndex(index)}
                    onClick={() => run(command)}
                    className={cn(
                      'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-[13px] transition-colors',
                      isActive ? 'bg-surface-2 text-foreground' : 'text-muted-foreground',
                    )}
                  >
                    <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
                    <span className="flex-1 truncate">{command.label}</span>
                    <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
                      {command.group}
                    </span>
                    {isActive && <CornerDownLeft className="size-3 text-faint" aria-hidden="true" />}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
