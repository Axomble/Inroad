import { useRef } from 'react'
import { Search, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useHotkey } from '@/hooks/use-hotkey'

/**
 * The filter box for a list toolbar. Compact by design — it sits in a 40px
 * `SectionBar`, so it can't use the full-height `Input`.
 *
 * Focus is bound to `/`, the convention every mail client and code host shares,
 * and `useHotkey` already refuses to fire while the user is typing elsewhere so
 * the shortcut can't swallow a literal slash. Escape clears and blurs, which is
 * the only way out that doesn't require reaching for the mouse.
 */
export function ListSearchInput({
  value,
  onChange,
  placeholder = 'Search…',
  /** Announced count, e.g. "3 of 24" — rendered inside the field, right-aligned. */
  hint,
  className,
}: {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  hint?: string
  className?: string
}) {
  const inputRef = useRef<HTMLInputElement>(null)

  useHotkey({ key: '/' }, () => inputRef.current?.focus())

  return (
    <div className={cn('relative flex h-7 items-center', className)}>
      <Search
        className="pointer-events-none absolute left-2 size-3.5 text-faint"
        strokeWidth={1.75}
        aria-hidden="true"
      />
      <input
        ref={inputRef}
        type="search"
        role="searchbox"
        value={value}
        placeholder={placeholder}
        aria-label={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== 'Escape') return
          // Stop the list's own Escape binding from also firing, so one press
          // does one thing.
          e.stopPropagation()
          if (value) onChange('')
          else e.currentTarget.blur()
        }}
        className={cn(
          'h-7 w-44 rounded-md border border-input bg-surface-2 pl-7 text-[12.5px] text-foreground',
          'shadow-[inset_0_1px_2px_var(--input-inset)] outline-none transition-colors',
          'placeholder:text-faint focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40',
          // The native search affordance duplicates our own clear button.
          '[&::-webkit-search-cancel-button]:appearance-none',
          value ? 'pr-7' : 'pr-2',
        )}
      />
      {value ? (
        <button
          type="button"
          onClick={() => {
            onChange('')
            inputRef.current?.focus()
          }}
          aria-label="Clear search"
          className="absolute right-1.5 rounded p-0.5 text-faint transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="size-3" aria-hidden="true" />
        </button>
      ) : (
        hint && (
          <span className="pointer-events-none absolute right-2 font-mono text-[10px] tabular-nums text-faint">
            {hint}
          </span>
        )
      )}
    </div>
  )
}
