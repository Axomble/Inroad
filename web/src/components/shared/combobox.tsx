import { useId, useState, type KeyboardEvent, type UIEvent } from 'react'
import { Check, Loader2, Search, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** One choosable row. `detail` disambiguates two similar labels (a domain, say). */
export interface ComboboxOption {
  id: string
  label: string
  detail?: string
}

/** The state of the result list as a whole — the first page for the current query. */
export interface ComboboxListState {
  loading: boolean
  /** A finished sentence. The component names no domain, so the caller writes the copy. */
  error?: string
  onRetry?: () => void
}

/** Appending further pages. Omit it for a list that is always complete. */
export interface ComboboxPaging {
  hasMore: boolean
  loading: boolean
  error?: string
  onLoadMore: () => void
}

export interface ComboboxLabels {
  placeholder: string
  /** Names the result list for assistive tech, e.g. "Companies". */
  list: string
  /** Shown when the list loaded and holds nothing. */
  empty: string
  /** What an empty selection means, e.g. "No company". */
  none: string
  /** Accessible name of the button that clears the selection. */
  clear: string
  /** Optional guidance under the field, e.g. a minimum query length. */
  hint?: string
}

/** Pixels from the bottom of the list at which scrolling asks for the next page. */
const loadMoreThreshold = 32

/**
 * A search box over a server-filtered list of options, with a single selection.
 *
 * It fetches nothing and knows no domain: the caller owns the query (and any
 * debouncing), the options, and the paging, and passes finished copy for every
 * state. That keeps it in `components/shared` and lets one implementation serve
 * any picker whose options are too many for a `<select>` — a native select can
 * only offer what it was given, which is how a workspace's 201st company became
 * unreachable.
 *
 * The selection is rendered from `selected` itself, not looked up in `options`,
 * so the current choice stays visible whether or not it is on a loaded page or
 * matches the current query.
 *
 * The list is always shown rather than popped over: this sits inside an edit
 * panel the user opened on purpose, so a second open/close step would be one
 * more thing between them and the choice.
 */
export function Combobox({
  inputId,
  query,
  onQueryChange,
  options,
  selected,
  onSelect,
  list,
  paging,
  labels,
  disabled = false,
}: {
  inputId: string
  query: string
  onQueryChange: (next: string) => void
  options: readonly ComboboxOption[]
  selected: ComboboxOption | null
  onSelect: (option: ComboboxOption | null) => void
  list: ComboboxListState
  paging?: ComboboxPaging
  labels: ComboboxLabels
  disabled?: boolean
}) {
  const listId = useId()
  const hintId = useId()
  const [activeIndex, setActiveIndex] = useState(-1)
  // Clamped at render rather than reset in an effect: a shorter result set simply
  // pulls the highlight back onto the last row that exists.
  const active = activeIndex >= 0 ? options[Math.min(activeIndex, options.length - 1)] : undefined
  const optionId = (option: ComboboxOption) => `${listId}-${option.id}`

  const highlight = (index: number) => {
    setActiveIndex(index)
    const option = options[index]
    if (!option) return
    // aria-activedescendant keeps focus in the input, so the browser will not
    // scroll the list for us; jsdom has no scrollIntoView at all.
    const node = document.getElementById(optionId(option))
    if (node && typeof node.scrollIntoView === 'function') node.scrollIntoView({ block: 'nearest' })
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (options.length === 0) return
    const current = Math.min(activeIndex, options.length - 1)
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      highlight(Math.min(current + 1, options.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      highlight(Math.max(current - 1, 0))
    } else if (event.key === 'Enter' && active) {
      // A form around this must not submit on the Enter that picks a row.
      event.preventDefault()
      onSelect(active)
    }
  }

  const onScroll = (event: UIEvent<HTMLUListElement>) => {
    if (!paging?.hasMore || paging.loading || paging.error) return
    const { scrollHeight, scrollTop, clientHeight } = event.currentTarget
    if (scrollHeight - scrollTop - clientHeight <= loadMoreThreshold) paging.onLoadMore()
  }

  const showEmpty = !list.loading && !list.error && options.length === 0

  return (
    <div className="grid gap-1.5">
      <div className="flex min-h-7 items-center gap-2 text-sm" aria-live="polite">
        <span className="text-muted-foreground">Selected:</span>
        {selected ? (
          <>
            <span className="font-medium text-foreground">{selected.label}</span>
            <button
              type="button"
              onClick={() => onSelect(null)}
              disabled={disabled}
              aria-label={labels.clear}
              className="rounded p-0.5 text-faint transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
            >
              <X className="size-3.5" aria-hidden="true" />
            </button>
          </>
        ) : (
          <span className="text-muted-foreground">{labels.none}</span>
        )}
      </div>

      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-faint" aria-hidden="true" />
        <input
          id={inputId}
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={active ? optionId(active) : undefined}
          aria-describedby={labels.hint ? hintId : undefined}
          autoComplete="off"
          value={query}
          placeholder={labels.placeholder}
          disabled={disabled}
          onChange={(event) => {
            setActiveIndex(-1)
            onQueryChange(event.target.value)
          }}
          onKeyDown={onKeyDown}
          className={cn(
            'flex h-9 w-full min-w-0 rounded-md border border-input bg-surface-2 py-1 pr-3 pl-8 text-sm text-foreground outline-none transition-colors',
            'shadow-[inset_0_1px_2px_var(--input-inset)] placeholder:text-faint',
            'focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50',
          )}
        />
      </div>
      {labels.hint ? (
        <p id={hintId} className="text-xs text-muted-foreground">
          {labels.hint}
        </p>
      ) : null}

      <ul
        id={listId}
        role="listbox"
        aria-label={labels.list}
        aria-busy={list.loading || paging?.loading ? true : undefined}
        onScroll={onScroll}
        className="max-h-56 overflow-y-auto rounded-md border border-input empty:hidden"
      >
        {options.map((option, index) => {
          const isSelected = option.id === selected?.id
          return (
            <li
              key={option.id}
              id={optionId(option)}
              role="option"
              aria-selected={isSelected}
              // Clicking must not steal focus from the input, or keyboard users
              // who reach for the mouse once lose their place in the list.
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setActiveIndex(index)}
              onClick={() => {
                if (!disabled) onSelect(option)
              }}
              className={cn(
                'flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm',
                option === active ? 'bg-accent text-accent-foreground' : undefined,
                disabled ? 'cursor-not-allowed opacity-50' : undefined,
              )}
            >
              <span className="min-w-0 flex-1 truncate">
                {option.label}
                {option.detail ? <span className="ml-2 text-xs text-muted-foreground">{option.detail}</span> : null}
              </span>
              {isSelected ? <Check className="size-3.5 text-accent-ink" aria-hidden="true" /> : null}
            </li>
          )
        })}
      </ul>

      {list.loading ? (
        <p role="status" className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          Loading…
        </p>
      ) : null}
      {list.error ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-danger">
          <span>{list.error}</span>
          {list.onRetry ? (
            <Button type="button" variant="outline" size="sm" onClick={list.onRetry}>
              Retry
            </Button>
          ) : null}
        </div>
      ) : null}
      {showEmpty ? <p className="text-xs text-muted-foreground">{labels.empty}</p> : null}

      {paging?.error ? (
        <p role="alert" className="text-xs text-danger">
          {paging.error}
        </p>
      ) : null}
      {paging?.hasMore ? (
        <Button type="button" variant="ghost" size="sm" onClick={paging.onLoadMore} disabled={paging.loading || disabled}>
          {paging.loading ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          Load more
        </Button>
      ) : null}
    </div>
  )
}
