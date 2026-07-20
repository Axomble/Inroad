import type { ErrorComponentProps } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'

/**
 * Router's default error component: rendered when a route loader/beforeLoad
 * throws. Kept minimal — the shell (header + sidebar) is still on-screen so we
 * just show a short message plus a reload button.
 */
export function RouteError({ error, reset }: ErrorComponentProps) {
  const message = error instanceof Error ? error.message : 'Something went wrong.'
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-danger">Error</p>
      <h1 className="text-lg font-semibold tracking-tight text-foreground">
        We couldn't load this page.
      </h1>
      <p className="max-w-sm text-xs text-muted-foreground">{message}</p>
      <Button variant="primary" size="sm" className="mt-2" onClick={() => reset()}>
        Try again
      </Button>
    </div>
  )
}
