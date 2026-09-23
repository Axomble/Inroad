import { Plus } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * The canvas's one "put something here" control. Icon-only, so the label is
 * required: it has to say where the new node goes ("Add a step after step 2"),
 * because on a canvas the position is the whole meaning of the action.
 */
export function AddNodeButton({
  label,
  onClick,
  className,
}: {
  label: string
  onClick: () => void
  className?: string
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className={cn(
        'nodrag nopan flex size-6 cursor-pointer items-center justify-center rounded-full border border-border-strong bg-surface text-muted-foreground outline-none transition-colors hover:border-primary-edge hover:bg-primary hover:text-primary-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
    >
      <Plus className="size-3.5" aria-hidden="true" />
    </button>
  )
}
