import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

export const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap w-fit [&_svg]:size-3 [&_svg]:pointer-events-none',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-primary/15 text-primary',
        warm: 'border-transparent bg-warm/15 text-warm',
        ok: 'border-transparent bg-ok/15 text-ok',
        danger: 'border-transparent bg-danger/15 text-danger',
        outline: 'border-border-strong text-muted-foreground',
        secondary: 'border-transparent bg-surface-2 text-muted-foreground',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {
  asChild?: boolean
  ref?: React.Ref<HTMLSpanElement>
}

export function Badge({ className, variant, asChild = false, ref, ...props }: BadgeProps) {
  const Comp = asChild ? Slot : 'span'
  return <Comp ref={ref} data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />
}
