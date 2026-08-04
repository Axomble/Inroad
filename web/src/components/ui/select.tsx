import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'

type SelectProps = React.SelectHTMLAttributes<HTMLSelectElement> & {
  ref?: React.Ref<HTMLSelectElement>
  /** Width/positioning for the wrapper (defaults to full width, for form grids). */
  wrapperClassName?: string
}

/**
 * Native `<select>` wearing the same control skin as `Input` — same height,
 * border, recessed fill, and focus ring — so a form row of mixed controls reads
 * as one set instead of two. `appearance-none` plus our own chevron replaces the
 * platform arrow, which is the one part of a native select the tokens can't
 * reach and the part that makes it read as browser chrome instead of the app's.
 *
 * Native rather than a Radix listbox on purpose: these are short, plain option
 * lists (a role, a rotation mode, the IANA timezone list), and the platform
 * control brings keyboard model, typeahead, and mobile pickers for free. Reach
 * for a custom menu only when options need to render richer than text.
 */
export function Select({ className, wrapperClassName, ref, ...props }: SelectProps) {
  return (
    <span data-slot="select-wrapper" className={cn('relative inline-flex w-full min-w-0', wrapperClassName)}>
      <select
        ref={ref}
        data-slot="select"
        className={cn(
          'flex h-9 w-full min-w-0 appearance-none rounded-md border border-input bg-surface-2 py-1 pl-2.5 pr-8 text-sm text-foreground shadow-[inset_0_1px_2px_var(--input-inset)] transition-colors outline-none',
          'focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40',
          'disabled:cursor-not-allowed disabled:opacity-50',
          'aria-invalid:border-danger aria-invalid:ring-2 aria-invalid:ring-danger/30',
          className,
        )}
        {...props}
      />
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute right-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
      />
    </span>
  )
}
