import { Download, Trophy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { httpStatus } from '@/lib/rtk-error'
import { useGetCampaignResultsQuery, type CampaignResultRow, type CampaignStepResults } from './api'

/** Renders a ratio as a percentage with one decimal — 0.0123 → "1.2%". */
function percent(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`
}

/**
 * Per-step, per-variant results for one campaign.
 *
 * Reply rate is the column the eye should land on, so it is emphasised and the
 * winner badge hangs off it: opens are proxy-inflated and structurally zero with
 * tracking off, and clicks measure whether the copy has a link. The other
 * columns are there because they explain a reply rate, not because they rank
 * anything.
 *
 * The winner line is shown whether or not there IS one, because "too close to
 * call" is the answer an operator most needs and the one a blank space hides.
 */
export function ResultsPanel({ campaignId }: { campaignId: string }) {
  const { data, isLoading, isError, error, refetch } = useGetCampaignResultsQuery({ id: campaignId })

  if (isLoading) return <LoadingTable />
  if (isError) {
    const status = httpStatus(error)
    return (
      <div className="space-y-2 px-5 py-6">
        <p className="text-sm text-muted-foreground">
          {status === 503
            ? 'Reporting isn’t available on this server.'
            : `Couldn’t load results${status ? ` (${status})` : ''}.`}
        </p>
        {status !== 503 && (
          <Button variant="outline" size="sm" onClick={() => void refetch()}>
            Retry
          </Button>
        )}
      </div>
    )
  }

  const steps = data?.steps ?? []
  if (steps.length === 0) {
    return (
      <p className="px-5 py-6 text-sm text-muted-foreground">
        Nothing has sent yet. Results appear per step as the campaign runs.
      </p>
    )
  }

  return (
    <div className="space-y-5 px-5 py-4">
      <div className="flex justify-end">
        {/* A plain link, not a fetch: the browser's own download handling is what
            turns a text/csv response into a saved file, and routing it through
            RTK Query would mean buffering the whole export in memory to hand it
            back to an anchor anyway. */}
        <Button asChild variant="outline" size="sm">
          <a href={`/api/v1/campaigns/${campaignId}/results.csv`} download>
            <Download className="size-4" />
            Export CSV
          </a>
        </Button>
      </div>

      {steps.map((step) => (
        <StepResults key={step.step_order} step={step} />
      ))}

      <p className="text-xs text-muted-foreground">
        Replies, bounces and unsubscribes are counted against the last message a contact received — the one they
        answered. On a multi-step campaign they concentrate on whichever step landed them.
      </p>
    </div>
  )
}

function StepResults({ step }: { step: CampaignStepResults }) {
  const isTest = step.rows.length > 1
  return (
    <section className="space-y-2">
      <header className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-sm font-medium">Step {step.step_order}</h3>
        <span className="truncate text-xs text-muted-foreground">{step.subject || 'Threads onto the previous email'}</span>
      </header>

      <div className="overflow-x-auto">
        <table className="w-full min-w-[42rem] text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">Variant</th>
              <th className="py-1.5 pr-3 text-right font-medium">Sent</th>
              <th className="py-1.5 pr-3 text-right font-medium">Opens</th>
              <th className="py-1.5 pr-3 text-right font-medium">Clicks</th>
              <th className="py-1.5 pr-3 text-right font-medium">Replies</th>
              <th className="py-1.5 pr-3 text-right font-medium">Bounced</th>
              <th className="py-1.5 text-right font-medium">Unsub</th>
            </tr>
          </thead>
          <tbody>
            {step.rows.map((row) => (
              <ResultRow key={row.variant_id ?? 'base'} row={row} isWinner={!!step.winner && step.winner === row.label} />
            ))}
          </tbody>
        </table>
      </div>

      {isTest && (
        <p className="text-xs text-muted-foreground">
          {step.winner ? (
            <>
              Variant <strong>{step.winner}</strong> is clearly ahead on reply rate.
            </>
          ) : (
            step.winner_note
          )}
        </p>
      )}
    </section>
  )
}

function ResultRow({ row, isWinner }: { row: CampaignResultRow; isWinner: boolean }) {
  return (
    <tr className="border-b border-border last:border-b-0">
      <td className="py-1.5 pr-3">
        <span className="inline-flex items-center gap-1.5">
          <span className="font-medium">{row.label}</span>
          {isWinner && (
            <Badge variant="outline" className="gap-1">
              <Trophy className="size-3" />
              Winner
            </Badge>
          )}
          {/* A retired arm still carries its results, so it stays in the table
              with an explanation rather than disappearing and taking its
              numbers with it. */}
          {row.weight === 0 && !row.is_base && <span className="text-xs text-muted-foreground">(paused)</span>}
        </span>
      </td>
      <td className="py-1.5 pr-3 text-right tabular-nums">{row.sent}</td>
      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">
        {row.opens} <span className="text-xs">({percent(row.open_rate)})</span>
      </td>
      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">
        {row.clicks} <span className="text-xs">({percent(row.click_rate)})</span>
      </td>
      <td className="py-1.5 pr-3 text-right font-medium tabular-nums">
        {row.replies} <span className="text-xs text-muted-foreground">({percent(row.reply_rate)})</span>
      </td>
      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">{row.bounces}</td>
      <td className="py-1.5 text-right tabular-nums text-muted-foreground">{row.unsubscribes}</td>
    </tr>
  )
}

function LoadingTable() {
  return (
    <div className="space-y-3 px-5 py-4">
      <Skeleton className="h-4 w-32" />
      {[0, 1, 2].map((i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  )
}
