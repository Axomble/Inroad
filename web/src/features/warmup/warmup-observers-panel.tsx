import { useId } from 'react'
import { MutedEmpty } from '@/components/shared/record-page'
import type { WarmupMailbox, WarmupOverview } from '@/store/api'
import {
  OBSERVERS_INTRO,
  OBSERVERS_NOTHING_EXCLUDED,
  observersReading,
  type ObserverReading,
  type ObserverStat,
} from './observer-copy'

/**
 * Which mailboxes report far more of the mail they receive as spam than their
 * peers on the same receiving provider do.
 *
 * On the pool's own screen, above the mailbox list, and deliberately not inside a
 * per-mailbox disclosure: a verdict is a comparison BETWEEN mailboxes, so the one
 * place it cannot live is on any single card — and the mailbox it names is the
 * RECIPIENT, whose own card is about its sending, which is the reading this panel
 * most has to avoid.
 *
 * Nothing else in the system reads this field (security.md invariant 59), so this
 * panel is the entire feature. Every word of it comes from `observer-copy`,
 * including which of the two empty answers is true: the distinction this exists to
 * preserve — a suspicion that must never read as a sanction, over evidence that
 * was not removed — is a copy decision, and JSX is where those get flattened into
 * a red chip.
 *
 * Rendered eagerly, like the incidents panel beside it: the data arrived with the
 * overview that drew this page, it has no request of its own, and it is above the
 * fold, so a chunk boundary here would buy a skeleton and nothing else.
 */
export function WarmupObserversPanel({
  observers,
  pool,
}: {
  /** Undefined only while there is no overview at all: loading, or a failed fetch. */
  observers: WarmupOverview['discounted_observers'] | undefined
  /** The overview's own rows, used to name an observer by its email rather than its id. */
  pool: readonly WarmupMailbox[]
}) {
  const reading = observersReading(observers, pool)
  const headingId = useId()

  // No verdict was published at all: a server predating observer trust, or an
  // overview that never arrived. "No mailbox stands out" would claim a comparison
  // nobody ran.
  if (reading.kind === 'unreported') return null

  return (
    <section
      data-slot="warmup-observers"
      aria-labelledby={headingId}
      className="border-b border-border bg-surface/40 px-4 py-3 sm:px-5"
    >
      <h2 id={headingId} className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        Spam reporting outliers
      </h2>

      {reading.kind === 'flagged' ? (
        <>
          <p className="mb-2 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{OBSERVERS_INTRO}</p>

          {/*
            Above the rows, not under them — the only panel in this feature that
            puts its qualifier first. The others qualify a figure an operator is
            in the middle of reading; this one corrects a conclusion ("my spam
            evidence was thrown away") that a list of flagged mailboxes has
            already produced by the end of the first row.
          */}
          <p
            data-slot="observers-nothing-excluded"
            className="mb-3 max-w-prose border-l border-border pl-3 text-[11.5px] leading-snug text-muted-foreground"
          >
            {OBSERVERS_NOTHING_EXCLUDED}
          </p>

          <ul className="space-y-3">
            {reading.observers.map((observer) => (
              <Observer key={observer.key} observer={observer} />
            ))}
          </ul>
        </>
      ) : (
        // A real answer, not an apology — and the payload cannot say more than
        // this, which is why the copy does not try to.
        <MutedEmpty text={reading.message} />
      )}
    </section>
  )
}

/**
 * One observer: which mailbox, what it was compared against, and the arithmetic
 * that called it an outlier.
 *
 * A neutral left rule rather than the incident panel's warn-toned one, and the
 * difference is deliberate: an incident is degradation that is already happening,
 * where this is a mailbox nothing has been done about — colouring it as a fault
 * states the sanction the copy spends four sentences denying.
 */
function Observer({ observer }: { observer: ObserverReading }) {
  return (
    <li className="border-l border-border pl-3">
      <p className="min-w-0">
        <span data-slot="observer-finding" className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
          Reporting more spam than its peers
        </span>
        <span
          data-slot="observer-mailbox"
          className="mt-0.5 block truncate font-mono text-[12.5px] text-foreground"
          title={observer.mailbox}
        >
          {observer.mailbox}
        </span>
      </p>

      <p data-slot="observer-comparison" className="mt-0.5 text-[11px] leading-snug text-muted-foreground">
        {observer.comparison}
      </p>

      {/*
        Bounded rather than full-width: the three figures are one comparison and
        have to be read together, and spread across a desktop the peer rate ends
        up far enough from the mailbox's own to be read as a separate fact.
      */}
      <dl className="mt-1.5 grid max-w-3xl gap-x-4 gap-y-1.5 sm:grid-cols-3">
        {observer.stats.map((stat) => (
          <Stat key={stat.label} stat={stat} />
        ))}
      </dl>

      {observer.repeated && (
        <p data-slot="observer-repeated" className="mt-1.5 max-w-prose text-[10.5px] leading-snug text-faint">
          {observer.repeated}
        </p>
      )}
    </li>
  )
}

/**
 * One figure with its label. All three render alike on purpose: the multiple is
 * not the verdict, and typographic emphasis on it would turn the row back into the
 * badge it exists instead of.
 */
function Stat({ stat }: { stat: ObserverStat }) {
  return (
    <div className="min-w-0">
      <dt className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{stat.label}</dt>
      <dd>
        <span data-slot="observer-stat" className="block text-[12px] leading-snug tabular-nums text-foreground">
          {stat.value}
        </span>
        {stat.detail && (
          <span className="mt-0.5 block max-w-[22rem] text-[10.5px] leading-snug text-faint">{stat.detail}</span>
        )}
      </dd>
    </div>
  )
}
