import { useId } from 'react'
import { MutedEmpty } from '@/components/shared/record-page'
import {
  SENTINEL_CONFIDENCE_GATES_NOTHING,
  sentinelPoolReading,
  type SentinelPoolFacts,
} from './sentinel-copy'

/**
 * What this workspace has designated as measurement references, and what that
 * costs the pool.
 *
 * On the pool's own screen, above the mailbox list, for the reason the observers
 * panel is: the subject is the POOL, not any one participant. How many sentinels
 * there are, and whether they have grown past the advised share, are facts about
 * the arrangement — a per-card rendering would repeat one sentence n times and
 * still not answer "how much of this pool is measuring itself".
 *
 * Two things it must not become, both of which a plausible rendering reaches for:
 *
 *   NOT A SETUP PROMPT. Zero sentinels is the ordinary arrangement — most
 *   self-hosted installations never designate one, and warmup works exactly as it
 *   does without them. The empty state explains what one would buy and stops
 *   there.
 *
 *   NOT AN ALERT. The oversized note is advisory and nothing is refused, so it is
 *   rendered as a note. A sentence announced through `role="alert"` is a sanction
 *   whatever its words say, and this one exists precisely because refusing to pair
 *   would stop warmup rather than tell the operator something.
 *
 * The not-a-penalty note rides along in BOTH branches, because it is needed most
 * in the empty one: with no sentinel designated, every card below reads
 * "peer-only", and that is the reading this feature most has to keep from becoming
 * a defect to chase.
 *
 * Rendered eagerly, like the two panels beside it: the data arrived with the
 * overview that drew this page, it has no request of its own, and it is above the
 * fold.
 */
export function WarmupSentinelsPanel({ count, oversized, share, pool }: SentinelPoolFacts) {
  const reading = sentinelPoolReading({ count, oversized, share, pool })
  const headingId = useId()

  // The server said nothing about sentinels, or there is no pool yet. Silence,
  // because "no sentinel is designated" would be an answer to a question this
  // payload never asked.
  if (reading.kind === 'unreported') return null

  return (
    <section
      data-slot="warmup-sentinels"
      aria-labelledby={headingId}
      className="border-b border-border bg-surface/40 px-4 py-3 sm:px-5"
    >
      <h2 id={headingId} className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        Measurement sentinels
      </h2>

      {reading.kind === 'none' ? (
        // A real answer, not an empty state to apologise for.
        <MutedEmpty text={reading.message} />
      ) : (
        <>
          <p data-slot="sentinel-summary" className="max-w-prose text-[11.5px] leading-snug text-muted-foreground">
            {reading.summary}
          </p>

          <ul className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1">
            {reading.sentinels.map((mailbox) => (
              <li
                key={mailbox}
                data-slot="sentinel-mailbox"
                className="max-w-full truncate font-mono text-[12px] text-foreground"
                title={mailbox}
              >
                {mailbox}
              </li>
            ))}
          </ul>

          {reading.advisory && (
            // A neutral left rule, the same one the observers panel gives a
            // mailbox nothing has been done about — and deliberately not the
            // incident panel's warn tone. Colouring this as a fault states the
            // enforcement the sentence spends its length denying.
            <p
              data-slot="sentinel-advisory"
              className="mt-2 max-w-prose border-l border-border pl-3 text-[11.5px] leading-snug text-muted-foreground"
            >
              {reading.advisory}
            </p>
          )}
        </>
      )}

      <p
        data-slot="sentinel-gates-nothing"
        className="mt-2 max-w-prose text-[10.5px] leading-snug text-faint"
      >
        {SENTINEL_CONFIDENCE_GATES_NOTHING}
      </p>
    </section>
  )
}
