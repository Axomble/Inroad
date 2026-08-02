import { cn } from '@/lib/utils'

type SelectProps = React.SelectHTMLAttributes<HTMLSelectElement> & { ref?: React.Ref<HTMLSelectElement> }

/**
 * Native `<select>` wearing the same control skin as `Input` — same height,
 * border, recessed fill, and focus ring — so a form row of mixed controls reads
 * as one set instead of two.
 *
 * Native rather than a Radix listbox on purpose: these are short, plain option
 * lists (a role, a rotation mode, the IANA timezone list), and the platform
 * control brings keyboard model, typeahead, and mobile pickers for free. Reach
 * for a custom menu only when options need to render richer than text.
 */
export function Select({ className, ref, ...props }: SelectProps) {
  return (
    <select
      ref={ref}
      data-slot="select"
      className={cn(
        'flex h-9 w-full min-w-0 rounded-md border border-input bg-surface-2 px-2.5 py-1 text-sm text-foreground shadow-[inset_0_1px_2px_var(--input-inset)] transition-colors outline-none',
        'focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40',
        'disabled:cursor-not-allowed disabled:opacity-50',
        'aria-invalid:border-danger aria-invalid:ring-2 aria-invalid:ring-danger/30',
        className,
      )}
      {...props}
    />
  )
}
