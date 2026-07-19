import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

/**
 * Tactile button. Depth is a hard bottom "lip" + inset highlight (see the
 * `.tactile` layer in globals.css), not a soft shadow: it stands proud, lifts
 * on hover, and recesses on press. Each variant sets the three --tactile-*
 * custom properties that the layer turns into the physics.
 *
 * Two families share the same physics:
 *   - full controls (primary/warm/secondary/outline/ghost/destructive)
 *   - `chip` for toolbar filters (shallower lip)
 */
export const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 shrink-0 whitespace-nowrap rounded-md text-sm font-semibold cursor-pointer outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        primary:
          'tactile text-primary-foreground border-transparent [--tactile-top:var(--primary-top)] [--tactile-bot:var(--primary-bot)] [--tactile-edge:var(--primary-edge)]',
        warm: 'tactile text-warm-foreground border-transparent [--tactile-top:var(--warm-top)] [--tactile-bot:var(--warm-bot)] [--tactile-edge:var(--warm-edge)]',
        secondary:
          'tactile text-foreground [--tactile-top:var(--surface-2)] [--tactile-bot:var(--surface)] [--tactile-edge:var(--control-edge)]',
        destructive:
          'tactile text-destructive-foreground border-transparent [--tactile-top:var(--danger)] [--tactile-bot:var(--danger)] [--tactile-edge:#a5323c]',
        outline:
          'border border-border-strong bg-transparent text-foreground hover:bg-surface-2',
        ghost: 'text-muted-foreground hover:bg-accent hover:text-accent-foreground',
        link: 'text-primary underline-offset-4 hover:underline',
        chip: 'tactile tactile-shallow rounded-lg text-foreground font-medium [--tactile-top:var(--surface-2)] [--tactile-bot:var(--surface)] [--tactile-edge:var(--control-edge)]',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-[13px]',
        xs: 'h-7 rounded-md px-2.5 text-[12.5px]',
        lg: 'h-10 rounded-md px-6',
        icon: 'size-9',
        'icon-sm': 'size-8',
        chip: "h-8 px-3 text-[12.5px] font-medium [&_svg:not([class*='size-'])]:size-3.5",
      },
    },
    defaultVariants: { variant: 'secondary', size: 'default' },
  },
)

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  /** Render as the single child element (Radix Slot) instead of a <button>. */
  asChild?: boolean
  ref?: React.Ref<HTMLButtonElement>
}

export function Button({ className, variant, size, asChild = false, ref, ...props }: ButtonProps) {
  const Comp = asChild ? Slot : 'button'
  return (
    <Comp
      ref={ref}
      data-slot="button"
      data-variant={variant ?? 'secondary'}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  )
}
