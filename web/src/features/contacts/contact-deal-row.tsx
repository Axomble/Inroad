import { Link } from '@tanstack/react-router'
import { formatMoney } from '@/lib/money'
import type { ContactDeal } from './api'

/**
 * A deal this contact is the primary contact on, linked to its record. Use inside
 * a `<ul>`.
 *
 * Its CRM sibling (`features/crm/deal-row.tsx`) renders the same idea over
 * `CrmDeal`. They are kept apart rather than unified because a deal is a CRM
 * concept and this page is not CRM: sharing the row would mean either the
 * contacts feature importing CRM UI — the coupling this split exists to remove —
 * or a "deal" component sitting in a module that is supposed to know nothing
 * about deals. The shapes differ too: `ContactDeal` carries no pipeline name.
 */
export function ContactDealRow({ deal }: { deal: ContactDeal }) {
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
          {/* Colour is decoration; the label is what states the stage. */}
          <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: deal.stage_color }} aria-hidden="true" />
          {deal.stage_label}
          {deal.stage_is_won ? <span className="font-medium text-ok">Won</span> : null}
          {deal.stage_is_lost ? <span className="font-medium text-muted-foreground">Lost</span> : null}
        </p>
      </div>
      <p className="font-mono text-sm tabular-nums">
        {deal.amount_micros == null ? '—' : formatMoney(deal.amount_micros, deal.currency)}
      </p>
    </li>
  )
}
