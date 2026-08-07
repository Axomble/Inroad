import { Lock, Pencil, Trash2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { ReplyLabel } from './api'
import { LABEL_FLAGS } from './label-flags'

/**
 * One label row, shared by the drag-reorderable list and the static Suspense
 * fallback so both render identically — only the presence of `dragHandle`
 * differs. The color swatch is decorative reinforcement (the label text is the
 * signal), and builtins carry a text "Built-in" badge rather than a bare icon.
 */
export function LabelRowBody({
  label,
  dragHandle,
  onEdit,
  onDelete,
}: {
  label: ReplyLabel
  dragHandle?: React.ReactNode
  onEdit: () => void
  onDelete: () => void
}) {
  const flags = LABEL_FLAGS.filter((f) => label[f.field])
  return (
    <div className="flex items-center gap-3 border-b border-border px-5 py-3">
      {dragHandle}
      <span
        aria-hidden="true"
        className="size-3.5 shrink-0 rounded-full border border-border-strong"
        style={{ backgroundColor: label.color }}
      />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{label.label}</span>
          {label.is_builtin && (
            <Badge variant="secondary">
              <Lock aria-hidden="true" />
              Built-in
            </Badge>
          )}
          {flags.map((flag) => (
            <Badge key={flag.field} variant="outline" title={flag.description}>
              {flag.badge}
            </Badge>
          ))}
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-faint">{label.key}</div>
      </div>

      <Button variant="outline" size="sm" aria-label={`Edit label ${label.label}`} onClick={onEdit}>
        <Pencil className="size-3.5" />
        Edit
      </Button>
      {label.is_builtin ? (
        <TooltipProvider>
          <Tooltip>
            {/* A disabled button emits no pointer events, so the tooltip hangs
                off a focusable wrapper that can still receive them. */}
            <TooltipTrigger asChild>
              <span tabIndex={0} className="rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                <Button variant="outline" size="sm" disabled aria-label={`Delete label ${label.label} (built-in, cannot be deleted)`}>
                  <Trash2 className="size-3.5" />
                  Delete
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>
              Built-in labels can be renamed and recolored, but never deleted — the classifier and historical
              enrollments reference their key.
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : (
        <Button variant="outline" size="sm" aria-label={`Delete label ${label.label}`} onClick={onDelete}>
          <Trash2 className="size-3.5" />
          Delete
        </Button>
      )}
    </div>
  )
}
