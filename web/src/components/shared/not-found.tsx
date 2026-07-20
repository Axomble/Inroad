import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'

/** Router's default not-found screen — used by the root route. */
export function NotFound() {
  return (
    <div className="flex h-dvh flex-col items-center justify-center gap-3 p-6 text-center">
      <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">404</p>
      <h1 className="text-2xl font-semibold tracking-tight text-foreground">Page not found</h1>
      <p className="max-w-sm text-sm text-muted-foreground">
        The page you were looking for doesn't exist or was moved.
      </p>
      <Button asChild variant="primary" size="sm" className="mt-2">
        <Link to="/">Go home</Link>
      </Button>
    </div>
  )
}
