import { NodeViewWrapper, type ReactNodeViewProps } from '@tiptap/react'
import { cn } from '@/lib/utils'
import { tokenText } from './merge-tags'
import { useVariableContext } from './variable-context'

const STATUS_COPY = {
  known: 'Merge field',
  unknown: 'Unknown merge field — it won’t be filled in',
  unverified: 'Merge field (not checked yet)',
} as const

/**
 * A merge field as one atomic, non-editable token. The chip shows the exact
 * `{{name}}` that will be sent — a friendly label would hide the typo this
 * chip exists to expose. An unknown token also carries a visible "?" and a
 * spoken label, so the warning never rests on colour alone.
 */
export function VariableChip({ node, selected }: ReactNodeViewProps) {
  const { variables, classify } = useVariableContext()
  const name = String(node.attrs.name ?? '')
  const status = classify(name)
  const label = variables.find((v) => v.name === name)?.label
  const description = label ? `${STATUS_COPY[status]}: ${label}` : STATUS_COPY[status]

  return (
    <NodeViewWrapper
      as="span"
      data-variable-status={status}
      title={description}
      aria-label={`${tokenText(name)} — ${description}`}
      // Pre, so a mistyped "{{ first_name }}" shows its spaces.
      style={{ whiteSpace: 'pre' }}
      className={cn(
        'mx-px inline-flex items-center gap-0.5 rounded-sm border px-1 py-px align-baseline font-mono text-[0.85em] leading-tight select-none',
        status === 'known' && 'border-primary/30 bg-primary/10 text-accent-ink',
        status === 'unverified' && 'border-dashed border-border-strong bg-surface text-muted-foreground',
        status === 'unknown' && 'border-danger/50 bg-danger/10 text-danger',
        selected && 'ring-2 ring-ring/50',
      )}
    >
      {tokenText(name)}
      {status === 'unknown' && (
        <span aria-hidden="true" className="font-sans font-bold">
          ?
        </span>
      )}
    </NodeViewWrapper>
  )
}
