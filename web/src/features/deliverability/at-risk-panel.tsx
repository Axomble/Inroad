import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { SectionBar } from '@/components/layout/page'
import { StatusDot } from '@/components/shared/status-pill'
import type { AtRiskItem } from './api'

/**
 * The mailboxes and domains dragging the score down, each linking to the screen
 * where it can actually be fixed. The reason is always rendered: a list of bare
 * addresses under the word "at risk" tells an operator nothing to act on.
 */
export function AtRiskPanel({
  label,
  items,
  to,
  emptyCopy,
}: {
  label: string
  items: AtRiskItem[]
  to: '/app/mailboxes'
  emptyCopy: string
}) {
  return (
    <section aria-label={label} className="border-b border-border">
      <SectionBar label={label} count={items.length} />
      {items.length === 0 ? (
        <p className="px-4 py-4 text-sm text-muted-foreground sm:px-5">{emptyCopy}</p>
      ) : (
        <ul>
          {items.map((item) => (
            <li key={item.label} className="border-b border-border last:border-b-0">
              <Link
                to={to}
                className="flex items-start gap-2.5 px-4 py-3 transition-colors hover:bg-surface sm:px-5"
                aria-label={`${item.label} — ${item.reason}. Open Mailboxes.`}
              >
                <StatusDot tone="failing" className="mt-1.5" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13.5px] font-medium text-foreground">{item.label}</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">{item.reason}</span>
                </span>
                <ArrowUpRight className="mt-0.5 size-3.5 shrink-0 text-faint" aria-hidden="true" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
