import { useRef } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface ViewTab<Id extends string> {
  id: Id
  label: string
  icon: LucideIcon
}

/**
 * Tabs implemented as the full WAI-ARIA pattern: roving tabindex (one tab stop
 * for the set), arrow/Home/End selection, and `aria-controls` pointing at the
 * panel the page renders. Declaring the roles without the keyboard behaviour
 * would promise a screen-reader user something that then does nothing.
 *
 * Tabs rather than routes wherever the topbar, stat strip and create form are
 * shared across the views — splitting those into routes would duplicate the
 * shell once per view for a URL nothing links to.
 */
export function ViewTabs<Id extends string>({
  baseId,
  label,
  tabs,
  view,
  onSelect,
}: {
  /** Prefix for the tab and panel ids; the panel must be `${baseId}-panel-${id}`. */
  baseId: string
  label: string
  tabs: ReadonlyArray<ViewTab<Id>>
  view: Id
  onSelect: (next: Id) => void
}) {
  const tabRefs = useRef(new Map<Id, HTMLButtonElement>())

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const current = tabs.findIndex((tab) => tab.id === view)
    let index = current
    switch (event.key) {
      case 'ArrowRight':
        index = (current + 1) % tabs.length
        break
      case 'ArrowLeft':
        index = (current - 1 + tabs.length) % tabs.length
        break
      case 'Home':
        index = 0
        break
      case 'End':
        index = tabs.length - 1
        break
      default:
        return
    }
    const next = tabs[index]
    if (!next) return
    event.preventDefault()
    onSelect(next.id)
    tabRefs.current.get(next.id)?.focus()
  }

  return (
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className="flex min-h-11 items-center gap-1 border-b border-border px-4 sm:px-5"
    >
      {tabs.map(({ id, label: tabLabel, icon: Icon }) => (
        <button
          key={id}
          ref={(node) => {
            if (node) tabRefs.current.set(id, node)
            else tabRefs.current.delete(id)
          }}
          type="button"
          role="tab"
          id={`${baseId}-tab-${id}`}
          aria-selected={view === id}
          aria-controls={`${baseId}-panel-${id}`}
          tabIndex={view === id ? 0 : -1}
          onClick={() => onSelect(id)}
          className={cn(
            'flex min-h-9 items-center gap-2 rounded-md px-3 text-sm text-muted-foreground outline-none transition-colors hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
            view === id && 'bg-surface-2 font-medium text-foreground',
          )}
        >
          <Icon className="size-4" aria-hidden="true" />
          {tabLabel}
        </button>
      ))}
    </div>
  )
}
