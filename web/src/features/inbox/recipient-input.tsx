import { useState } from 'react'
import { X } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import { looksLikeEmail, splitRecipients, mergeRecipients } from './recipient-parsing'

/**
 * A recipient field: committed addresses as removable chips, plus a live input.
 *
 * Chips rather than a comma-joined string because splitting a string back into
 * addresses is exactly the parsing that mangles a display name containing a
 * comma — and because a wrong address should be removable individually rather
 * than by re-editing a run-on line.
 */
export function RecipientInput({
  id,
  label,
  values,
  onChange,
  placeholder,
}: {
  id: string
  label: string
  values: readonly string[]
  onChange: (next: string[]) => void
  placeholder?: string
}) {
  const [typing, setTyping] = useState('')

  const commit = (raw: string) => {
    const { committed, remainder } = splitRecipients(raw)
    if (committed.length > 0) {
      const merged = mergeRecipients(values, committed)
      if (merged.length !== values.length) onChange(merged)
    }
    setTyping(remainder)
  }

  /** Commits whatever is typed — on blur, or on Enter/Tab. */
  const commitAll = () => {
    const trimmed = typing.trim()
    if (trimmed === '') return
    commit(trimmed + ',')
  }

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1">
      <label htmlFor={id} className="w-10 shrink-0 font-mono text-[10px] tracking-wide text-faint uppercase">
        {label}
      </label>
      {values.map((value) => (
        <span
          key={value}
          className={cn(
            'inline-flex max-w-[14rem] items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px]',
            looksLikeEmail(value)
              ? 'border-border bg-surface-2 text-foreground'
              : 'border-danger/40 bg-danger/10 text-danger',
          )}
        >
          <span className="truncate">{value}</span>
          {!looksLikeEmail(value) && <span className="sr-only">(not a valid address)</span>}
          <button
            type="button"
            aria-label={`Remove ${value}`}
            className="shrink-0 rounded hover:text-danger focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
            onClick={() => onChange(values.filter((v) => v !== value))}
          >
            <X className="size-2.5" />
          </button>
        </span>
      ))}
      <Input
        id={id}
        value={typing}
        placeholder={values.length === 0 ? placeholder : undefined}
        aria-label={label}
        className="h-7 min-w-[8rem] flex-1 border-0 bg-transparent px-1 text-[12px] shadow-none focus-visible:ring-0"
        onChange={(e) => commit(e.target.value)}
        onBlur={commitAll}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === 'Tab') {
            // Only intercept Tab when there is something to commit, so an empty
            // field still moves focus the way a keyboard user expects.
            if (e.key === 'Tab' && typing.trim() === '') return
            e.preventDefault()
            commitAll()
            return
          }
          // Backspace on an empty field removes the last chip — the behaviour
          // every chip input has, and the fastest way to fix a mistyped address.
          if (e.key === 'Backspace' && typing === '' && values.length > 0) {
            onChange(values.slice(0, -1))
          }
        }}
      />
    </div>
  )
}
