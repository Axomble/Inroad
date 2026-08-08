import { Link } from '@tanstack/react-router'
import type { ContactDeal } from '@/store/api'
import { formatMoney } from './money'

/**
 * The fields a deal row renders, derived from the generated `ContactDeal` rather
 * than restated. `CrmDeal` — what the board and the company's deal list return —
 * satisfies this structurally and adds `pipeline_name`, so one row component
 * serves every list of deals in the app.
 */
export type DealRowFields = Pick<
  ContactDeal,
  'id' | 'name' | 'stage_label' | 'stage_color' | 'stage_is_won' | 'stage_is_lost' | 'amount_micros' | 'currency'
> & { pipeline_name?: string }

/** One deal, linked to its record. Use inside a `<ul>`. */
export function DealRow({ deal }: { deal: DealRowFields }) {
  return (
    <li className="flex min-w-0 flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-background p-3">
      <div className="min-w-0">
        <Link
          to="/app/deals/$id"
          params={{ id: deal.id }}
          className="text-sm font-medium text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          {deal.name}
        </Link>
        <p className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          {/* The stage carries a colour, but the colour is decoration: the label
              is always present, so the stage never depends on hue. Won and lost
              get a word too — they are the two states a reader scans for. */}
          <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: deal.stage_color }} aria-hidden="true" />
          {deal.stage_label}
          {deal.stage_is_won ? <span className="font-medium text-ok">Won</span> : null}
          {deal.stage_is_lost ? <span className="font-medium text-muted-foreground">Lost</span> : null}
          {deal.pipeline_name ? <span className="text-faint">/ {deal.pipeline_name}</span> : null}
        </p>
      </div>
      <p className="font-mono text-sm tabular-nums">
        {deal.amount_micros == null ? '—' : formatMoney(deal.amount_micros, deal.currency)}
      </p>
    </li>
  )
}
