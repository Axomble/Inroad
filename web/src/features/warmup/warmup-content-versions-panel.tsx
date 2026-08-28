import { useId } from 'react'
import { MutedEmpty } from '@/components/shared/record-page'
import type { WarmupOverview } from '@/store/api'
import {
  contentVersionsReading,
  VERSIONS_GATES_NOTHING,
  VERSIONS_INTRO,
  type VersionFigure,
  type VersionReading,
} from './content-version-copy'

/**
 * How each template in the warmup library placed, across the whole pool.
 *
 * On the pool's own screen and deliberately NOT inside a per-mailbox disclosure:
 * the split is per workspace because the library is shared, so on a mailbox card
 * every row would read as a statement about that one sender — which is precisely
 * the confusion the panel exists to remove.
 *
 * Every word comes from `content-version-copy`, including which of the two empty
 * answers is true. The distinction this preserves — a figure that is confounded by
 * WHO sent the template, over one that is merely thin — is a copy decision, and JSX
 * is where those get flattened into a percentage in a cell.
 *
 * Rendered eagerly, like the observers and incidents panels beside it: the data
 * arrived with the overview that drew this page, it has no request of its own, so a
 * chunk boundary here would buy a skeleton and nothing else.
 */
export function WarmupContentVersionsPanel({
  versions,
}: {
  /** Undefined only while there is no overview at all: loading, or a failed fetch. */
  versions: WarmupOverview['content_versions'] | undefined
}) {
  const reading = contentVersionsReading(versions)
  const headingId = useId()

  // Nothing was published at all: a server predating content versions, or an
  // overview that never arrived. "Nothing observed yet" would describe a window
  // nobody measured.
  if (reading.kind === 'unreported') return null

  return (
    <section
      data-slot="warmup-content-versions"
      aria-labelledby={headingId}
      className="border-b border-border bg-surface/40 px-4 py-3 sm:px-5"
    >
      <h2 id={headingId} className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        Placement by template
      </h2>

      {reading.kind === 'observed' ? (
        <>
          <p className="mb-2 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{VERSIONS_INTRO}</p>

          <ul className="space-y-3">
            {reading.versions.map((version) => (
              <Version key={version.key} version={version} />
            ))}
          </ul>

          {/*
            Below the rows: unlike the observers panel's qualifier, this one
            corrects a conclusion the reader forms only once they have a disparity
            in front of them. Above the table it would be a caveat about numbers
            nobody has seen yet.
          */}
          <p
            data-slot="versions-gates-nothing"
            className="mt-3 max-w-prose border-l border-border pl-3 text-[11px] leading-snug text-faint"
          >
            {VERSIONS_GATES_NOTHING}
          </p>

          {reading.soleNote && (
            <p
              data-slot="versions-sole-note"
              className="mt-2 max-w-prose border-l border-border pl-3 text-[11px] leading-snug text-faint"
            >
              {reading.soleNote}
            </p>
          )}
        </>
      ) : (
        // A real answer, not an apology.
        <MutedEmpty text={reading.message} />
      )}
    </section>
  )
}

/**
 * One template: which one, the evidence behind it, and its two rates.
 *
 * The counts line sits above the rates on purpose. Below the sample floor the fold
 * withholds the rates and keeps the counts, so for most rows the counts are the
 * only real content — putting them second would bury the evidence under two cells
 * that both read "Not established".
 */
function Version({ version }: { version: VersionReading }) {
  return (
    <li className="border-l border-border pl-3">
      <p className="min-w-0">
        <span data-slot="version-kind" className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
          Template fingerprint
        </span>
        <span
          data-slot="version-label"
          className="mt-0.5 block truncate font-mono text-[12.5px] text-foreground"
          title={version.fingerprint || undefined}
        >
          {version.label}
        </span>
      </p>

      <p data-slot="version-counts" className="mt-0.5 text-[11px] leading-snug tabular-nums text-muted-foreground">
        {version.counts}
      </p>

      {/*
        Bounded rather than full-width, like the observers panel: the two rates are
        one reading over a shared denominator and have to be read together.
      */}
      <dl className="mt-1.5 grid max-w-3xl gap-x-4 gap-y-1.5 sm:grid-cols-2">
        {version.figures.map((figure) => (
          <Figure key={figure.label} figure={figure} />
        ))}
      </dl>
    </li>
  )
}

/**
 * One figure with its label. Measured and unmeasured render alike: a template
 * whose rate is "Not established" is not a worse template, and typographic
 * emphasis on the ones that happen to have a number would rank rows by how much
 * mail they drew.
 */
function Figure({ figure }: { figure: VersionFigure }) {
  return (
    <div className="min-w-0">
      <dt className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{figure.label}</dt>
      <dd>
        <span data-slot="version-figure" className="block text-[12px] leading-snug tabular-nums text-foreground">
          {figure.value}
        </span>
        <span data-slot="version-population" className="mt-0.5 block text-[10.5px] leading-snug text-faint">
          {figure.population}
        </span>
        {figure.detail && (
          <span className="mt-0.5 block max-w-[22rem] text-[10.5px] leading-snug text-faint">{figure.detail}</span>
        )}
      </dd>
    </div>
  )
}
