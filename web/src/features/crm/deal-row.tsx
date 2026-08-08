import { Link } from '@tanstack/react-router'
import { formatMoney } from '@/lib/money'
import type { CrmDeal } from './api'

/**
 * One deal, linked to its record. Use inside a `<ul>`.
 *
 * Deliberately *not* in the shared record module: a deal is a CRM record type,
 * not a polymorphic attachment. It knows stages, pipelines, money and the
 * `/app/deals` route, none of which a neutral module should carry. The contact
 * record renders its own row, over the contacts API's own `ContactDeal` shape,
 * which has no pipeline.
 */
export function DealRow({ deal }: { deal: CrmDeal }) {
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
          <span className="text-faint">/ {deal.pipeline_name}</span>
        </p>
      </div>
      <p className="font-mono text-sm tabular-nums">
        {deal.amount_micros == null ? '—' : formatMoney(deal.amount_micros, deal.currency)}
      </p>
    </li>
  )
}
