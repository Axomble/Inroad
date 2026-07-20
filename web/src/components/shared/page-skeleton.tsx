import { Skeleton } from '@/components/ui/skeleton'

/**
 * Full-page loading state used as the router's default pending component and
 * as the outer Suspense fallback in `main.tsx`. Intentionally quiet — the app
 * shell renders inside this so we just show a few placeholder rows.
 */
export function PageSkeleton() {
  return (
    <div className="flex h-full flex-col gap-2 p-5">
      <Skeleton className="h-4 w-40" />
      <Skeleton className="h-8 w-full max-w-md" />
      <div className="mt-4 space-y-2">
        <Skeleton className="h-3 w-full" />
        <Skeleton className="h-3 w-5/6" />
        <Skeleton className="h-3 w-2/3" />
      </div>
    </div>
  )
}
