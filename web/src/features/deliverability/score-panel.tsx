import { cn } from '@/lib/utils'
import { StatusPill } from '@/components/shared/status-pill'
import { componentCopies, scoreHeadline, type ComponentCopy } from '@/lib/deliverability-copy'
import type { DeliverabilityScore } from './api'

/**
 * The score, as the one hero figure on the page, plus every component broken out.
 *
 * Two things this component refuses to do, both from `lib/deliverability-copy`:
 * it never colours the number as healthy when confidence is low (the sentence
 * beside it says the sample is too small, and the figure itself goes faint), and
 * it never renders an unmeasured component as a clean zero.
 */
export function ScorePanel({ score }: { score: DeliverabilityScore }) {
  const headline = scoreHeadline(score)
  const components = componentCopies(score)

  return (
    <section aria-label="Deliverability score" className="border-b border-border bg-surface/60">
      <div className="flex flex-col gap-4 px-4 py-6 sm:flex-row sm:items-start sm:gap-8 sm:px-6">
        <div className="min-w-0">
          {/* Proportional figures, not tabular: this is a display-size standalone
              number, and equal-width digits make it look loose. */}
          <div
            className={cn(
              'text-[68px] font-light leading-none tracking-[-0.045em]',
              // A provisional score is deliberately not in a verdict colour.
              headline.provisional ? 'text-faint' : 'text-foreground',
            )}
          >
            {headline.value}
            <span className="ml-1 align-top text-[18px] text-faint">/100</span>
          </div>
          <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1">
            <StatusPill tone={headline.tone}>{headline.label}</StatusPill>
            {headline.provisional && (
              <span className="font-mono text-[10.5px] uppercase tracking-[0.1em] text-warn">
                Small sample
              </span>
            )}
          </div>
        </div>

        {/* The qualifier is a sentence, not a badge — a badge next to a big green
            number is read as decoration, which is how "96 over 11 delivered" ends
            up mistaken for a clean bill of health. */}
        <p className="max-w-prose text-[13px] leading-relaxed text-muted-foreground sm:mt-1">
          {headline.qualifier}
        </p>
      </div>

      <ul className="grid gap-px bg-border md:grid-cols-2">
        {components.map((component) => (
          <ComponentRow key={component.key} component={component} />
        ))}
      </ul>
    </section>
  )
}

/**
 * One component. An unmeasured one is faint with an explicit "Not measured"
 * status — never the `ok` tone, and never a percentage, because there is no
 * measurement to render.
 */
function ComponentRow({ component }: { component: ComponentCopy }) {
  return (
    <li
      className="bg-surface px-4 py-3 sm:px-6"
      data-measured={component.measured}
      data-component={component.key}
    >
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <span className="text-[13.5px] font-medium text-foreground">{component.label}</span>
        <StatusPill tone={component.tone}>{component.status}</StatusPill>
        {component.penaltyLabel && (
          <span className="ml-auto font-mono text-[11px] tabular-nums text-muted-foreground">
            {component.penaltyLabel}
          </span>
        )}
      </div>
      <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{component.detail}</p>
    </li>
  )
}
