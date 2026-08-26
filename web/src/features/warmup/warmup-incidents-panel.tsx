import { useId } from 'react'
import { MutedEmpty } from '@/components/shared/record-page'
import type { WarmupMailbox, WarmupOverview } from '@/store/api'
import {
  INCIDENTS_GATES_NOTHING,
  INCIDENTS_INTRO,
  incidentsReading,
  type IncidentReading,
  type IncidentStat,
} from './incident-copy'

/**
 * Where degradation across the pool is concentrated in one shared value.
 *
 * On the pool's own screen, above the mailbox list, and deliberately not inside a
 * per-mailbox disclosure: an incident is a statement about several mailboxes at
 * once, so the one place it cannot live is on any single card — an operator would
 * have to open four of them and diff the panels by hand, which is precisely the
 * work this exists to remove.
 *
 * Every reading comes from `incident-copy`, including which of the four "no
 * incidents" answers is true. The distinction this panel exists to preserve — a
 * correlation that must never read as a cause — is a copy decision, and JSX is
 * where those get quietly flattened into a red badge.
 *
 * Rendered eagerly, unlike the route matrix and the history: its data arrived
 * with the overview that drew this page, it has no request of its own, and it is
 * above the fold, so a chunk boundary here would buy a skeleton and nothing else.
 */
export function WarmupIncidentsPanel({
  incidents,
  pool,
  minPool,
}: {
  /** Undefined only while there is no overview at all: loading, or a failed fetch. */
  incidents: WarmupOverview['incidents'] | undefined
  /** The overview's own rows — the pool an empty array has to be read against. */
  pool: readonly WarmupMailbox[]
  /**
   * The smallest pool detection can find anything in, served by the API rather
   * than hardcoded here. It is derived from a backend policy constant, and a copy
   * on this side would drift the moment that constant is recalibrated — leaving
   * this panel claiming it searched a pool the server never examined.
   */
  minPool: WarmupOverview['incidents_min_pool'] | undefined
}) {
  const reading = incidentsReading(incidents, pool, minPool)
  const headingId = useId()

  // No inference was made over anything: a server that does not report
  // incidents, or a workspace with no participants. Saying "no shared cause
  // found" would claim a search nobody ran.
  if (reading.kind === 'unreported') return null

  return (
    <section
      data-slot="warmup-incidents"
      aria-labelledby={headingId}
      className="border-b border-border bg-surface/40 px-4 py-3 sm:px-5"
    >
      <h2 id={headingId} className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        Correlated degradation
      </h2>

      {reading.kind === 'detected' ? (
        <>
          <p className="mb-3 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{INCIDENTS_INTRO}</p>

          <ul className="space-y-3">
            {reading.incidents.map((incident) => (
              <Incident key={incident.key} incident={incident} />
            ))}
          </ul>

          {reading.truncated && (
            <p data-slot="incident-truncated" className="mt-3 max-w-prose text-[10.5px] leading-snug text-faint">
              {reading.truncated}
            </p>
          )}

          <p className="mt-3 max-w-prose text-[10.5px] leading-snug text-faint">{INCIDENTS_GATES_NOTHING}</p>
        </>
      ) : (
        // A real answer, not an apology — and which of the answers it is comes
        // from the copy, because the empty array is identical in every case.
        <MutedEmpty text={reading.message} />
      )}
    </section>
  )
}

/**
 * One correlation: what is shared, and the arithmetic that called it
 * concentrated.
 *
 * The dimension heads the row and the value is its subject, the same way the
 * route matrix heads a row by its destination — an operator scans for "which of
 * my domains" and reads the counts second.
 */
function Incident({ incident }: { incident: IncidentReading }) {
  return (
    <li className="border-l border-warn/40 pl-3">
      <p className="min-w-0">
        <span data-slot="incident-dimension" className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
          {incident.dimension}
        </span>
        <span
          data-slot="incident-value"
          className="mt-0.5 block truncate font-mono text-[12.5px] text-foreground"
          title={incident.value}
        >
          {incident.value}
        </span>
      </p>

      {/*
        Bounded rather than full-width: the three figures are one comparison and
        have to be read together, and spread across a desktop the outside count
        ends up far enough from the inside one to be read as a separate fact.
      */}
      <dl className="mt-1.5 grid max-w-3xl gap-x-4 gap-y-1.5 sm:grid-cols-3">
        {incident.stats.map((stat) => (
          <Stat key={stat.label} stat={stat} />
        ))}
      </dl>

      {incident.members.length > 0 && (
        <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">
          <span className="text-faint">Degraded: </span>
          <span data-slot="incident-members" className="font-mono">
            {incident.members.join(', ')}
          </span>
        </p>
      )}

      <p className="mt-1 max-w-prose text-[10.5px] leading-snug text-faint">{incident.dimensionDetail}</p>
    </li>
  )
}

/**
 * One figure with its label. Both counts and the concentration render alike on
 * purpose: none of the three is the verdict, and typographic emphasis on the
 * lift would turn it back into the badge this row exists instead of.
 */
function Stat({ stat }: { stat: IncidentStat }) {
  return (
    <div className="min-w-0">
      <dt className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{stat.label}</dt>
      <dd>
        <span data-slot="incident-stat" className="block text-[12px] leading-snug tabular-nums text-foreground">
          {stat.value}
        </span>
        {stat.detail && (
          <span className="mt-0.5 block max-w-[22rem] text-[10.5px] leading-snug text-faint">{stat.detail}</span>
        )}
      </dd>
    </div>
  )
}
