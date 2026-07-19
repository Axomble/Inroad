import { cn } from '@/lib/utils'

type DivProps = React.HTMLAttributes<HTMLDivElement> & { ref?: React.Ref<HTMLDivElement> }

export function Card({ className, ref, ...props }: DivProps) {
  return (
    <div
      ref={ref}
      data-slot="card"
      className={cn('rounded-xl border border-border bg-card text-card-foreground', className)}
      {...props}
    />
  )
}

export function CardHeader({ className, ref, ...props }: DivProps) {
  return <div ref={ref} data-slot="card-header" className={cn('flex flex-col gap-1 p-5', className)} {...props} />
}

export function CardTitle({ className, ref, ...props }: DivProps) {
  return (
    <div ref={ref} data-slot="card-title" className={cn('font-semibold tracking-tight', className)} {...props} />
  )
}

export function CardDescription({ className, ref, ...props }: DivProps) {
  return (
    <div ref={ref} data-slot="card-description" className={cn('text-sm text-muted-foreground', className)} {...props} />
  )
}

export function CardContent({ className, ref, ...props }: DivProps) {
  return <div ref={ref} data-slot="card-content" className={cn('p-5 pt-0', className)} {...props} />
}

export function CardFooter({ className, ref, ...props }: DivProps) {
  return (
    <div ref={ref} data-slot="card-footer" className={cn('flex items-center p-5 pt-0', className)} {...props} />
  )
}
