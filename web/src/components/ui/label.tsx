import * as LabelPrimitive from '@radix-ui/react-label'
import { cn } from '@/lib/utils'

type LabelProps = React.ComponentProps<typeof LabelPrimitive.Root>

export function Label({ className, ...props }: LabelProps) {
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        'flex items-center gap-2 text-sm font-medium text-foreground select-none',
        'peer-disabled:cursor-not-allowed peer-disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}
