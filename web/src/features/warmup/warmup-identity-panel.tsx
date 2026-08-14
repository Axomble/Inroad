import { formatDateTime } from '@/lib/datetime'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import { MutedEmpty } from '@/components/shared/record-page'
import type { WarmupMailbox } from '@/store/api'
import {
  IDENTITY_GATES_NOTHING,
  IDENTITY_INTRO,
  IDENTITY_NOTHING_REPORTED,
  identityReading,
  type IdentityFact,
  type VerdictFact,
} from './identity-copy'

/**
 * What this mailbox's warmup mail was last seen going out as, and what the
 * partner that received it said about those signatures.
 *
 * Diagnostic detail, so it sits behind the card's own disclosure rather than on
 * the row: an operator triaging placement does not need six more figures in the
 * metrics line, and the answer this panel gives ("which signing domain is
 * failing") is only asked once something else already looks wrong.
 *
 * Every reading here comes from `identity-copy`, including the tones — the
 * distinctions this panel exists to preserve (`none` vs `unknown` above all) are
 * copy decisions, and putting them in JSX is how they get quietly flattened.
 *
 * Rendered eagerly, unlike its two lazy neighbours on this card: it issues no
 * request of its own and its data arrived with the overview row that drew the
 * card, so a chunk boundary here would buy a spinner and nothing else.
 */
export function WarmupIdentityPanel({ identity }: { identity: WarmupMailbox['identity'] }) {
  const reading = identityReading(identity)

  return (
    <div data-slot="warmup-identity" className="border-t border-border bg-surface/40 px-4 py-3 sm:px-5">
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Sending identity</p>
        {reading.kind === 'observed' && reading.observedAt && <ObservedAt iso={reading.observedAt} />}
      </div>

      {reading.kind === 'unobserved' ? (
        <MutedEmpty text={reading.message} />
      ) : (
        <>
          <p className="mb-3 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{IDENTITY_INTRO}</p>

          <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-2">
            {reading.facts.map((fact) => (
              <DomainFact key={fact.label} fact={fact} />
            ))}
          </dl>

          <p className="mt-3 mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
            What the receiver reported
          </p>
          <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-3">
            {reading.verdicts.map((verdict) => (
              <Verdict key={verdict.label} verdict={verdict} />
            ))}
          </dl>
          {reading.verdicts.every((verdict) => !verdict.reported) && (
            <p className="mt-2 max-w-prose text-[11px] leading-snug text-muted-foreground">
              {IDENTITY_NOTHING_REPORTED}
            </p>
          )}

          <p className="mt-3 text-[10.5px] leading-snug text-faint">{IDENTITY_GATES_NOTHING}</p>
        </>
      )}
    </div>
  )
}

/**
 * When the identity was seen. Load-bearing rather than chrome: this is the LAST
 * observation, so a verdict from three weeks ago describes three-week-old mail,
 * and the panel would otherwise present it as current.
 */
function ObservedAt({ iso }: { iso: string }) {
  return (
    <time dateTime={iso} className="font-mono text-[10.5px] text-muted-foreground">
      <span className="text-faint">observed </span>
      {formatDateTime(iso)}
      <span className="text-faint"> · {relativeTime(iso)}</span>
    </time>
  )
}

function DomainFact({ fact }: { fact: IdentityFact }) {
  return (
    <div className="min-w-0">
      <Label text={fact.label} />
      <dd>
        <span
          className={cn(
            'block truncate text-[12px]',
            // A domain is data and reads as data; the words standing in for an
            // absent one must not, or "Not signed" looks like a domain named
            // "Not signed".
            fact.recorded ? 'font-mono text-foreground' : 'text-muted-foreground',
          )}
          title={fact.recorded ? fact.value : undefined}
        >
          {fact.value}
        </span>
        <Detail text={fact.detail} />
      </dd>
    </div>
  )
}

function Verdict({ verdict }: { verdict: VerdictFact }) {
  return (
    <div className="min-w-0">
      <Label text={verdict.label} />
      <dd>
        <span className="flex items-start gap-1.5">
          {/*
            Shape, not colour, carries the one distinction this panel exists for:
            a filled node is a verdict somebody reported, a hollow one is the
            absence of any report. The words say it too — this is redundancy for
            a scanning eye, never the only signal.
          */}
          <span
            aria-hidden="true"
            className={cn(
              'mt-1.5 size-1.5 shrink-0 rounded-full',
              verdict.reported ? 'bg-current' : 'border border-current bg-transparent',
              verdict.tone,
            )}
          />
          <span data-slot="verdict-value" className={cn('text-[12px] leading-snug', verdict.tone)}>
            {verdict.value}
            {/* The tabbed rate's marker, verbatim, for the same reason: a failing
                verdict is the one an operator would act on, and nothing acts on
                it. Wrapped as a unit so "· gates" / "nothing" never split. */}
            {verdict.negative && <span className="whitespace-nowrap text-faint"> · gates nothing</span>}
          </span>
        </span>
        <Detail text={verdict.detail} />
      </dd>
    </div>
  )
}

function Label({ text }: { text: string }) {
  return <dt className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{text}</dt>
}

function Detail({ text }: { text: string }) {
  return <span className="mt-0.5 block text-[10.5px] leading-snug text-faint">{text}</span>
}
