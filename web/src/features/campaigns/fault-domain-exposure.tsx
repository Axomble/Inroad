import { useId } from 'react'
import { MutedEmpty } from '@/components/shared/record-page'
import { StatusPill, type StatusTone } from '@/components/shared/status-pill'
import { cn } from '@/lib/utils'
import type { CampaignSenderPool } from './api'
import {
  EXPOSURE_ADVISORY,
  EXPOSURE_INTRO,
  exposureReading,
  type DomainExposure,
  type ExposureTone,
} from './exposure-copy'

/**
 * How much of a campaign rests on any one domain that can fail all at once.
 *
 * On the senders panel, under the pool it is measured over, because the only
 * action it ever asks for is a change to that pool — putting it on its own
 * screen would separate the finding from the three ticks that answer it.
 *
 * Every reading, including which of the two "nothing to measure" answers is
 * true, comes from `exposure-copy`. The distinctions this exists to preserve —
 * a per-domain ceiling that is not the campaign's, and an over-budget row that
 * withheld nothing — are copy decisions, and JSX is where those get flattened
 * into a red badge that reads as a stoppage.
 */
export function FaultDomainExposure({ pool }: { pool: CampaignSenderPool | undefined }) {
  const reading = exposureReading(pool)
  const headingId = useId()

  // No pool, no senders, or a server that predates exposure reporting. Silence,
  // rather than a concentration figure nobody computed.
  if (reading.kind === 'unreported') return null

  return (
    <section
      data-slot="fault-domain-exposure"
      aria-labelledby={headingId}
      className="rounded-md border border-border bg-surface-2/40 px-3 py-2.5"
    >
      <h3 id={headingId} className="font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        Domain concentration
      </h3>

      {reading.kind === 'measured' ? (
        <>
          <p className="mt-1.5 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{EXPOSURE_INTRO}</p>

          <ul className="mt-2.5 space-y-2.5">
            {reading.domains.map((domain) => (
              <DomainRow key={domain.key} domain={domain} />
            ))}
          </ul>

          {reading.soleNote && (
            <p data-slot="exposure-sole" className="mt-2.5 max-w-prose text-[11px] leading-snug text-muted-foreground">
              {reading.soleNote}
            </p>
          )}

          {reading.uncovered && (
            <p data-slot="exposure-uncovered" className="mt-2.5 max-w-prose text-[10.5px] leading-snug text-faint">
              {reading.uncovered}
            </p>
          )}

          <p className="mt-2.5 max-w-prose text-[10.5px] leading-snug text-faint">{EXPOSURE_ADVISORY}</p>
        </>
      ) : (
        // A real answer, not an apology — and which of the two it is comes from
        // the copy, because an empty array looks identical in both cases.
        <MutedEmpty text={reading.message} />
      )}
    </section>
  )
}

/**
 * `StatusPill`'s tone vocabulary is the campaign lifecycle's; these are colour
 * roles, named once here so the mapping is a decision rather than an accident.
 * Amber for over budget deliberately, never red: nothing failed and nothing was
 * withheld.
 */
const PILL_TONE: Record<ExposureTone, StatusTone> = {
  over: 'paused',
  within: 'running',
  inapplicable: 'draft',
}

/** The meter's fill, on the same three colours as the pill beside it. */
const FILL: Record<ExposureTone, string> = {
  over: 'bg-warn',
  within: 'bg-ok',
  inapplicable: 'bg-muted-foreground',
}

const EDGE: Record<ExposureTone, string> = {
  over: 'border-warn/50',
  within: 'border-ok/40',
  inapplicable: 'border-border',
}

/**
 * One domain: what it carries, and the ceiling IT was judged against.
 *
 * The ceiling is on every row rather than stated once for the campaign, because
 * a degrading domain is held to a lower one — and a table that shows one shared
 * limit turns "25% is over, 55% is not" into an arithmetic error the operator is
 * right to distrust.
 */
function DomainRow({ domain }: { domain: DomainExposure }) {
  return (
    <li className={cn('border-l pl-2.5', EDGE[domain.tone])}>
      <p className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1">
        <span
          data-slot="exposure-domain"
          className="min-w-0 truncate font-mono text-[12.5px] text-foreground"
          title={domain.domain}
        >
          {domain.domain}
        </span>
        {/* The word is the signal; the dot and its colour only reinforce it. */}
        <StatusPill tone={PILL_TONE[domain.tone]}>{domain.status}</StatusPill>
      </p>

      {domain.meter && <Meter share={domain.meter.share} ceiling={domain.meter.ceiling} tone={domain.tone} />}

      <p className="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
        <span>
          <span data-slot="exposure-share" className="font-mono tabular-nums text-foreground">
            {domain.share}
          </span>
          <span className="text-faint"> of the campaign</span>
        </span>
        <span>
          <span className="text-faint">its ceiling </span>
          <span data-slot="exposure-ceiling" className="font-mono tabular-nums text-foreground">
            {domain.ceiling}
          </span>
        </span>
        <span data-slot="exposure-assigned">{domain.assigned}</span>
      </p>

      {domain.detail && (
        <p data-slot="exposure-detail" className="mt-0.5 max-w-prose text-[10.5px] leading-snug text-faint">
          {domain.detail}
        </p>
      )}
      {domain.tightened && (
        <p data-slot="exposure-tightened" className="mt-0.5 max-w-prose text-[10.5px] leading-snug text-faint">
          {domain.tightened}
        </p>
      )}
    </li>
  )
}

/**
 * Share against its own ceiling, at a glance. Hidden from the accessibility tree
 * on purpose: both figures are already text on the line below, and a bar that
 * announced itself would read them out twice with less precision.
 */
function Meter({ share, ceiling, tone }: { share: number; ceiling: number | null; tone: ExposureTone }) {
  return (
    <span
      data-slot="exposure-meter"
      aria-hidden="true"
      className="relative mt-1.5 block h-1.5 w-full max-w-[15rem] rounded-full bg-muted"
    >
      <span
        className={cn('absolute inset-y-0 left-0 rounded-full', FILL[tone])}
        style={{ width: `${share}%` }}
      />
      {ceiling !== null && (
        <span className="absolute -inset-y-1 w-px bg-foreground/70" style={{ left: `${ceiling}%` }} />
      )}
    </span>
  )
}
