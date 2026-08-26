import { Link } from '@tanstack/react-router'
import { formatMoney } from '@/lib/money'
// Type-only import across features: a type carries no code, so this creates no
// runtime coupling (the same allowance `crm/revert-stage-change.tsx` relies on).
import type { ContactDeal } from '@/features/contacts/api'

/**
 * One of this contact's deals, at side-rail density.
 *
 * Written here rather than imported, deliberately. `crm/deal-row.tsx` and
 * `contacts/contact-deal-row.tsx` already render this same idea twice, and both
 * docblocks explain why: a deal is a CRM concept, so sharing the row would mean
 * either a non-CRM feature importing CRM UI, or a "deal" component living in a
 * module that is supposed to know nothing about deals. This is the third
 * instance and the reasoning is unchanged — plus the inbox needs a materially
 * tighter row than either of them: the reader's rail is ~18rem wide, where the
 * full-page rows' two-line layout and 3rem money column do not fit.
 *
 * Use inside a `<ul>`.
 */
export function ContactDealChip({ deal }: { deal: ContactDeal }) {
  return (
    <li className="min-w-0 border-b border-border py-1.5 last:border-b-0">
      <Link
        to="/app/deals/$id"
        params={{ id: deal.id }}
        className="block truncate text-[12px] font-medium text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
      >
        {deal.name}
      </Link>
      <p className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground">
        {/* Colour is decoration; the label is what states the stage. */}
        <span
          className="size-1.5 shrink-0 rounded-full"
          style={{ backgroundColor: deal.stage_color }}
          aria-hidden="true"
        />
        <span className="min-w-0 truncate">{deal.stage_label}</span>
        {deal.stage_is_won && <span className="shrink-0 font-medium text-ok">Won</span>}
        {deal.stage_is_lost && <span className="shrink-0 font-medium text-muted-foreground">Lost</span>}
        {deal.amount_micros != null && (
          <span className="ml-auto shrink-0 font-mono tabular-nums">
            {formatMoney(deal.amount_micros, deal.currency)}
          </span>
        )}
      </p>
    </li>
  )
}
