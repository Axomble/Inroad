import { cn } from '@/lib/utils'

/**
 * A small text flag on a canvas node ("In a loop", "Not reached"). Always words,
 * so the state never rests on the tint alone.
 */
export function NodeFlag({ tone, children }: { tone: 'danger' | 'muted'; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        'shrink-0 rounded border px-1 py-px font-mono text-[9px] uppercase tracking-[0.1em]',
        tone === 'danger' ? 'border-danger/40 bg-danger/10 text-danger' : 'border-border bg-surface-2 text-faint',
      )}
    >
      {children}
    </span>
  )
}
