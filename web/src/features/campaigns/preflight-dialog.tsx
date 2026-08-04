import { AlertCircle, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog'
import { StatusPill, type StatusTone } from '@/components/shared/status-pill'
import { httpStatus } from '@/lib/rtk-error'
import { useGetCampaignPreflightQuery } from './api'
import type { CampaignPreflightCheck } from './api'

/**
 * Severity → StatusPill tone. `pass` borrows the "running"/ok tone (green),
 * `warn` the "paused"/amber tone, `fail` the "failing"/red tone — the same
 * palette the rest of the campaign UI already uses for state, so a fail here
 * reads exactly like a fail anywhere else. The pill's text label (below) is
 * what actually carries the meaning; the tone is reinforcement, not the only
 * signal.
 */
const SEVERITY_TONE: Record<CampaignPreflightCheck['severity'], StatusTone> = {
  pass: 'running',
  warn: 'paused',
  fail: 'failing',
}

const SEVERITY_LABEL: Record<CampaignPreflightCheck['severity'], string> = {
  pass: 'Pass',
  warn: 'Warn',
  fail: 'Fail',
}

/**
 * Gate a launch behind the campaign's readiness report. Fetches lazily — only
 * while `open` is true — so opening the campaigns list or a campaign's own
 * page never fires a preflight request nobody asked for.
 *
 * `refetchOnMountOrArgChange: true` is load-bearing, not decoration: the
 * server's own `launch` handler only re-validates status/steps/list-emptiness
 * on the way in, so THIS report is the only place sender-pool/domain-auth/
 * tracking/daily-limit/warmup ever get checked before a send starts. Without
 * forcing a fresh fetch, reopening the dialog within RTK Query's 60s
 * `keepUnusedDataFor` window (e.g. cancel, fix a sender, reopen seconds
 * later) would silently re-serve the stale cached report — `isFetching`
 * would read `false` and the primary action could enable on data that no
 * longer describes the campaign. Same reasoning, same fix as
 * `contacts-page.tsx`'s `useListContactsQuery` (nothing here goes through a
 * cache tag either, so no mutation can invalidate it for us).
 *
 * Deliberately does not own the launch mutation itself: `onConfirm` is the
 * caller's existing launch handler (already wired to its own inline error
 * copy — "Already launched.", "Target list is empty.", …), so a failed launch
 * still surfaces exactly where it did before this dialog existed. This dialog
 * only decides whether the operator is ALLOWED to try.
 */
export function PreflightDialog({
  open,
  onOpenChange,
  campaignId,
  campaignName,
  onConfirm,
  isLaunching = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  campaignId: string
  /** Falls back to a generic noun so the title never reads "Launch ""?". */
  campaignName?: string
  onConfirm: () => void
  isLaunching?: boolean
}) {
  const { data, isFetching, error, refetch } = useGetCampaignPreflightQuery(
    { id: campaignId },
    { skip: !open, refetchOnMountOrArgChange: true },
  )
  const name = campaignName ?? 'this campaign'
  const checks = data?.checks ?? []
  const hasWarn = checks.some((c) => c.severity === 'warn')
  const confirmDisabled = isFetching || error != null || !data || !data.ready || isLaunching
  const confirmLabel = data?.ready && hasWarn ? 'Launch anyway' : 'Launch campaign'

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-w-lg">
        <AlertDialogHeader>
          <AlertDialogTitle>Launch &ldquo;{name}&rdquo;?</AlertDialogTitle>
          <AlertDialogDescription>
            Inroad checked whether this campaign is ready to send. Review each check below before
            launching.
          </AlertDialogDescription>
        </AlertDialogHeader>

        {isFetching ? (
          <div className="space-y-2" aria-hidden="true">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : error ? (
          <div
            role="alert"
            className="flex items-center justify-between gap-3 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger"
          >
            <span className="flex items-center gap-2">
              <AlertCircle className="size-4 shrink-0" aria-hidden="true" />
              Couldn&rsquo;t load the readiness checks{httpStatus(error) ? ` (${httpStatus(error)})` : ''}.
            </span>
            <Button variant="ghost" size="xs" onClick={() => void refetch()}>
              Try again
            </Button>
          </div>
        ) : (
          <ul aria-label="Readiness checks" className="max-h-80 space-y-2 overflow-y-auto">
            {checks.map((check) => (
              <li key={check.id} className="rounded-md border border-border bg-surface-2/40 px-3 py-2.5">
                <div className="flex items-start justify-between gap-3">
                  <span className="text-sm font-medium text-foreground">{check.title}</span>
                  <StatusPill tone={SEVERITY_TONE[check.severity]} className="shrink-0">
                    {SEVERITY_LABEL[check.severity]}
                  </StatusPill>
                </div>
                <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{check.detail}</p>
                {check.severity !== 'pass' && check.remedy && (
                  <p className="mt-1 text-xs leading-relaxed text-foreground">{check.remedy}</p>
                )}
              </li>
            ))}
          </ul>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={confirmDisabled} onClick={() => onConfirm()}>
            {isLaunching && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
