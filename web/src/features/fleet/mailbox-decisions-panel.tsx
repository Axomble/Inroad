import { useState } from 'react'
import { EmptyBlock, SectionBar } from '@/components/layout/page'
import { Skeleton } from '@/components/ui/skeleton'
import { relativeTime } from '@/lib/relative-time'
import { useListFleetDecisionsQuery, type FleetDecision } from './api'
import {
  DECISIONS_EMPTY_DESCRIPTION,
  DECISIONS_EMPTY_TITLE,
  DECISIONS_INTRO,
  DECISIONS_PICK_PROMPT,
  decisionKindCopy,
  fleetErrorMessage,
  triggeredByLabel,
} from './fleet-copy'
// Read-only cross-feature hook import, which the conventions permit explicitly
// (hooks only, never components or state). This panel needs the workspace's
// mailboxes to offer a choice, and a mailbox list is the mailboxes feature's
// own contract — a second copy of it here is how the two would drift.
import { useListMailboxesQuery } from '@/features/mailboxes/api'

/**
 * "Why is this mailbox on this worker?"
 *
 * The decision log answers that and nothing had ever asked it: the query behind
 * this panel existed, was documented with the question it answers, and had no
 * caller outside an integration test.
 *
 * It is keyed on a mailbox because a decision is about one, so the panel opens
 * with a chooser rather than a list — there is no "all decisions" read, and
 * inventing one would mean a screen that pages through every placement the
 * deployment has ever made, which answers nobody's question.
 */
export function MailboxDecisionsPanel() {
  const [mailboxId, setMailboxId] = useState('')
  const { data, isLoading: loadingMailboxes } = useListMailboxesQuery()
  // `id` is optional on the generated Mailbox, so a mailbox without one is
  // dropped rather than rendered as an option that cannot be selected — an
  // `<option>` with an undefined value silently takes its label as its value,
  // which would put an email address where a UUID belongs.
  const mailboxes = (data ?? []).filter((mailbox) => mailbox.id !== undefined)

  return (
    <section data-slot="fleet-decisions">
      <SectionBar label="Placement history" />
      <p className="max-w-prose px-4 pt-3 text-[12px] leading-snug text-muted-foreground sm:px-5">
        {DECISIONS_INTRO}
      </p>

      <div className="px-4 py-3 sm:px-5">
        <label
          htmlFor="fleet-decision-mailbox"
          className="block font-mono text-[10px] uppercase tracking-[0.14em] text-faint"
        >
          Mailbox
        </label>
        <select
          id="fleet-decision-mailbox"
          data-slot="fleet-decision-picker"
          value={mailboxId}
          disabled={loadingMailboxes}
          onChange={(e) => setMailboxId(e.target.value)}
          className="mt-1.5 h-9 w-full max-w-md rounded-lg border border-border bg-surface px-2.5 text-[13px] text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <option value="">{loadingMailboxes ? 'Loading mailboxes…' : 'Choose a mailbox…'}</option>
          {mailboxes.map((mailbox) => (
            <option key={mailbox.id} value={mailbox.id}>
              {mailbox.email}
            </option>
          ))}
        </select>
      </div>

      {mailboxId === '' ? (
        <p className="px-4 pb-4 text-[12px] leading-snug text-faint sm:px-5">{DECISIONS_PICK_PROMPT}</p>
      ) : (
        <DecisionList mailboxId={mailboxId} />
      )}
    </section>
  )
}

/**
 * Split out so the query is only mounted once a mailbox is chosen. Rendering the
 * hook unconditionally with a `skip` would work too, but a separate component
 * keeps "no mailbox picked" out of the branches that have to reason about
 * loading and error state.
 */
function DecisionList({ mailboxId }: { mailboxId: string }) {
  const { data, isLoading, isError, error } = useListFleetDecisionsQuery({ id: mailboxId })
  const decisions = data?.decisions ?? []

  if (isLoading) {
    return (
      <ul>
        {[0, 1].map((i) => (
          <li key={i} className="border-b border-border px-4 py-3 sm:px-5">
            <div className="space-y-2">
              <Skeleton className="h-3.5 w-32" />
              <Skeleton className="h-2.5 w-72" />
            </div>
          </li>
        ))}
      </ul>
    )
  }

  if (isError) {
    // No list beneath it: an empty history under a failed read would say "this
    // mailbox has never been placed", which is a specific and wrong claim.
    return (
      <p
        role="alert"
        data-slot="fleet-decisions-error"
        className="px-4 py-4 text-[12px] leading-snug text-danger sm:px-5"
      >
        {fleetErrorMessage(error, "Couldn't load this mailbox's placement history. Refresh the page to try again.")}
      </p>
    )
  }

  if (decisions.length === 0) {
    return <EmptyBlock title={DECISIONS_EMPTY_TITLE} description={DECISIONS_EMPTY_DESCRIPTION} />
  }

  return (
    <ul>
      {decisions.map((decision) => (
        <DecisionRow key={decision.id} decision={decision} />
      ))}
    </ul>
  )
}

/**
 * One decision.
 *
 * THE REASON IS RENDERED AS WRITTEN — one string, in prose, not split into a
 * score column and a runner-up column. That is a constraint, not a layout
 * preference: these strings are built so that a decision which involved no
 * scoring never prints a comparison, because a forced placement has no runner-up
 * and rendering it against a 0.00 nobody computed makes the log lie. A table
 * with score columns would have to put something in those cells.
 */
function DecisionRow({ decision }: { decision: FleetDecision }) {
  const kind = decisionKindCopy(decision.kind)

  return (
    <li data-slot="fleet-decision" className="border-b border-border px-4 py-3 sm:px-5">
      <p className="flex flex-wrap items-center gap-2">
        <span
          data-slot="fleet-decision-kind"
          title={kind.detail}
          className="shrink-0 rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-[0.12em] text-muted-foreground"
        >
          {kind.label}
        </span>
        <span className="text-[12px] text-muted-foreground">
          {relativeTime(decision.created_at)} · {triggeredByLabel(decision.triggered_by)}
        </span>
        {/*
          Null when the decision named no destination — a refusal has none, and
          the copy says so rather than leaving a blank that reads as missing data.
        */}
        <span data-slot="fleet-decision-worker" className="font-mono text-[11px] text-faint">
          {decision.worker_id === null ? 'no worker' : decision.worker_id}
        </span>
      </p>
      <p data-slot="fleet-decision-reason" className="mt-1 max-w-prose text-[12.5px] leading-snug text-foreground">
        {decision.reason}
      </p>
    </li>
  )
}
