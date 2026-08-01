import { cn } from '@/lib/utils'

type TextareaProps = React.TextareaHTMLAttributes<HTMLTextAreaElement> & {
  ref?: React.Ref<HTMLTextAreaElement>
}

/** Multi-line counterpart to `Input`, sharing its control skin. */
export function Textarea({ className, ref, ...props }: TextareaProps) {
  return (
    <textarea
      ref={ref}
      data-slot="textarea"
      className={cn(
        'flex w-full min-w-0 resize-y rounded-md border border-input bg-surface-2 px-3 py-2 text-sm leading-relaxed text-foreground shadow-[inset_0_1px_2px_var(--input-inset)] transition-colors outline-none',
        'placeholder:text-faint selection:bg-primary selection:text-primary-foreground',
        'focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40',
        'disabled:cursor-not-allowed disabled:opacity-50',
        'aria-invalid:border-danger aria-invalid:ring-2 aria-invalid:ring-danger/30',
        className,
      )}
      {...props}
    />
  )
}
