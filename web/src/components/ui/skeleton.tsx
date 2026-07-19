import { cn } from '@/lib/utils'

/** Placeholder that reserves layout space so async content never shifts the page. */
export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="skeleton"
      className={cn('animate-pulse rounded-md bg-surface-2 motion-reduce:animate-none', className)}
      {...props}
    />
  )
}
