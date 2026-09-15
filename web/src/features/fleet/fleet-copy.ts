// The words and the arithmetic the fleet screen is made of.
//
// Kept out of JSX for the reason the dead-letter and warmup copy modules give:
// on an operator screen the wording IS the feature. Every number here is about
// infrastructure the reader does not own outright — a worker is shared, an
// egress IP's reputation is shared — and the two available mistakes are
// opposite. Read as alarming, an operator migrates mailboxes that were fine.
// Read as decoration, they miss the worker whose provider has started refusing
// its credentials.
//
// Four distinctions this module exists to hold:
//
//   AUTH FAILURES ARE NOT "ERRORS". A provider refusing the credential from an
//   address is the signal that predicts that address being challenged or
//   throttled; it is why these counters are collected at all. It gets its own
//   label and its own prominence, never a share of a generic failure total.
//
//   A REJECTION IS NOT THE WORKER'S FAULT. A permanent non-security 5xx is
//   about the recipient — a dead address, an oversized message. The contract
//   says so explicitly. It is shown, and it is shown APART from everything that
//   reflects on the IP, because an operator who reads it as worker health will
//   move mailboxes for no reason.
//
//   NO SIGNALS IS NOT ZERO SIGNALS. A worker that reported nothing in the
//   window is a different fact from one that reported only failures, and a row
//   of zeroes says the second when it means the first.
//
//   A DECISION'S REASON IS PROSE. It arrives already written to be acted on and
//   already constrained never to claim a score comparison that was not made.
//   This module does not parse it, split it, or lay it out in columns — doing
//   so would impose a structure it deliberately does not have.
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import type { FleetProviderSignals, ScheduledJob } from './api'

/* ------------------------------------------------------------- the screen */

export const PAGE_INTRO =
  'Every send authenticates to your own mail provider, and that provider sees the sending worker’s IP address on each attempt. It throttles that address, challenges sign-ins from it, and eventually refuses it. This is what your providers have been saying about the workers your mail leaves from.'

export const EMPTY_FLEET_TITLE = 'No mailbox is pinned to a worker yet'

export const EMPTY_FLEET_DESCRIPTION =
  'A mailbox is pinned to a worker the first time it sends, so its mail always leaves from one address. Nothing here has sent yet — this list fills itself.'

export const ADMINS_ONLY_TITLE = 'Admins only'

export const ADMINS_ONLY_DESCRIPTION =
  'Worker addresses and placement decisions are deployment infrastructure. Ask a workspace owner or admin to look at the fleet.'

/**
 * Said beside the signal counts, once, rather than repeated on every row.
 *
 * It is the one thing about these numbers a reader can get badly wrong: the
 * counters are per egress IP and a worker can carry several workspaces'
 * mailboxes, so they are not a count of this workspace's own traffic. Saying it
 * plainly is better than a smaller number that would be a lie.
 */
export const SHARED_FATE_NOTICE =
  'These counts are per worker address, not per mailbox: if other workspaces share a worker, their attempts are counted here too. That is the risk being measured — a provider’s opinion is about the address, not about whose mail triggered it.'

/* ----------------------------------------------------------- the liveness */

export interface LivenessCopy {
  label: string
  /** What this state means for mail pinned to the worker. */
  detail: string
}

export function livenessCopy(live: boolean): LivenessCopy {
  return live
    ? {
        label: 'Live',
        detail: 'Heartbeating. Mail pinned to this worker is being sent from its address.',
      }
    : {
        label: 'Not responding',
        detail:
          'This worker has not heartbeated recently enough to be used for new placements. Mail pinned to it is being moved to another worker — and until it is, it is not going out.',
      }
}

/* ------------------------------------------------------------ the numbers */

/**
 * A count as a share of a total, or null when there is no total to divide by.
 *
 * Null rather than 0 is the point: a percentage of nothing is not zero percent,
 * and "0%" beside "0 attempts" reads as a failure when it means silence. Every
 * caller is forced to decide what to render for the no-data case.
 */
export function percentOf(count: number, total: number): number | null {
  if (total <= 0) return null
  return (count / total) * 100
}

/**
 * A percentage for display beside — never instead of — the counts it came from.
 *
 * One decimal below 10% and none above it: the difference between 0.4% and 1.2%
 * auth failures is the whole signal, while the difference between 91% and 91.4%
 * success is noise an operator would read as precision that is not there.
 */
export function formatPercent(value: number | null): string {
  if (value === null) return '—'
  // Exactly none is "0%", not "0.0%": a decimal place implies a measurement
  // fine enough to have rounded, and this one did not round at all.
  if (value === 0) return '0%'
  if (value < 0.1) return '<0.1%'
  return `${value < 10 ? value.toFixed(1) : Math.round(value)}%`
}

/** Counts are read against each other, so they are grouped and never abbreviated into "1.2k". */
export function formatCount(n: number): string {
  return n.toLocaleString()
}

/**
 * A duration in milliseconds, for a sweep's runtime.
 *
 * Sub-second stays in milliseconds because that is the range a healthy sweep
 * lives in and rounding it to "0s" would erase the difference between a fast
 * one and a stalled one.
 */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return `${minutes}m ${seconds}s`
}

/**
 * The transport leg, named as an operator would say it rather than as the wire
 * spells it. `m365` in particular is an identifier, not a word.
 *
 * Falls back to the raw value: the contract may add a provider, and inventing a
 * label for one this build does not know would hide it.
 */
const PROVIDER_LABELS: Record<string, string> = {
  smtp: 'SMTP',
  gmail: 'Gmail',
  m365: 'Microsoft 365',
}

export function providerLabel(provider: string): string {
  return PROVIDER_LABELS[provider] ?? provider
}

const OPERATION_LABELS: Record<string, string> = {
  send: 'sending',
  poll: 'polling',
}

export function operationLabel(operation: string): string {
  return OPERATION_LABELS[operation] ?? operation
}

/**
 * How worrying a leg looks, at a glance.
 *
 * Deliberately derived from AUTH FAILURES AND BLOCKS ONLY, and deliberately not
 * from the success rate. A worker can have a poor success rate because its
 * mailboxes are sending to bad addresses — that is `rejected`, which is about
 * the recipient — and treating that as a sick worker is the specific
 * misreading the contract warns about. What makes a worker itself suspect is
 * the provider refusing its credential or refusing the address.
 *
 * The thresholds are a rendering choice and nothing more: no placement decision
 * is made from them, and the raw counts are always shown beside the colour, so a
 * reader who disagrees with where the line sits can still see everything.
 */
export type SignalSeverity = 'quiet' | 'healthy' | 'watch' | 'bad'

export function signalSeverity(signals: FleetProviderSignals): SignalSeverity {
  if (signals.attempts === 0) return 'quiet'
  if (signals.blocked > 0) return 'bad'
  const authRate = percentOf(signals.auth_failures, signals.attempts) ?? 0
  if (authRate >= 5) return 'bad'
  if (authRate > 0 || signals.throttled > 0) return 'watch'
  return 'healthy'
}

/**
 * The one-line reading of a leg, for the screen-reader and for the operator who
 * wants prose rather than a colour. Every branch names the number it is about,
 * because "degraded" on its own is not something anyone can act on.
 */
export function signalSummary(signals: FleetProviderSignals): string {
  const what = `${providerLabel(signals.provider)} ${operationLabel(signals.operation)}`
  if (signals.attempts === 0) {
    return `${what}: nothing reported in this window. That is silence, not success.`
  }
  if (signals.blocked > 0) {
    return `${what}: the provider refused this address ${formatCount(signals.blocked)} time${signals.blocked === 1 ? '' : 's'}. Mail from this worker is not getting through.`
  }
  if (signals.auth_failures > 0) {
    return `${what}: ${formatCount(signals.auth_failures)} of ${formatCount(signals.attempts)} attempts had the credential refused. This is what precedes an address being challenged or blocked.`
  }
  if (signals.throttled > 0) {
    return `${what}: the provider slowed this address down ${formatCount(signals.throttled)} time${signals.throttled === 1 ? '' : 's'}. Not a failure yet, but it is the first step toward one.`
  }
  return `${what}: ${formatCount(signals.successes)} of ${formatCount(signals.attempts)} attempts accepted, nothing refused.`
}

/* ------------------------------------------------------------- the sweeps */

export const JOBS_INTRO =
  'Periodic background sweeps — advancing enrollments, polling inboxes, checking domain authentication. A sweep that quietly stops running is otherwise invisible, so each one reports its last run whether or not that was recent.'

export const JOBS_EMPTY_TITLE = 'No sweep has reported yet'

export const JOBS_EMPTY_DESCRIPTION =
  'Sweeps record a row each time they run. An empty list means none has run since this deployment started — if it stays empty, the worker is not running.'

/**
 * Why there is no error message on a failing sweep, said where an operator
 * looks for one.
 *
 * Without this the screen reads as though it is hiding something or as though
 * the failure had no cause. The reason is a real constraint: the stored text
 * comes from handlers that may have wrapped another workspace's data, and the
 * ledger has no tenant to scope a read by.
 */
export const JOB_ERROR_TEXT_WITHHELD =
  'The error text is not shown here: a sweep runs across every workspace, so its message can name another one’s data. Look it up in the deployment logs using the time above.'

export interface JobStatusCopy {
  label: string
  tone: 'ok' | 'warn' | 'bad'
  detail: string
}

/**
 * How a sweep is doing, from the three facts the API reports.
 *
 * The order of the branches is the priority an operator cares about, and
 * "stopped" comes FIRST — ahead of "failing" — because a sweep that is not
 * running at all reports its last outcome as whatever it was when it stopped,
 * which for a silently-dead sweep is usually `ok`. Checking the outcome first
 * would paint the worst case green.
 */
export function jobStatusCopy(job: ScheduledJob): JobStatusCopy {
  if (job.runs_in_window === 0) {
    return {
      label: 'Not running',
      tone: 'bad',
      detail:
        'This sweep has not run at all in the window below. Its last run is shown with the time it happened — the work it does is not being done.',
    }
  }
  if (job.failures_in_window > 0) {
    const share = formatPercent(percentOf(job.failures_in_window, job.runs_in_window))
    return {
      label: job.last_outcome === 'error' ? 'Failing' : 'Recovering',
      tone: job.last_outcome === 'error' ? 'bad' : 'warn',
      detail:
        job.last_outcome === 'error'
          ? `The most recent run failed, and ${formatCount(job.failures_in_window)} of ${formatCount(job.runs_in_window)} runs (${share}) failed in this window.`
          : `The most recent run succeeded, but ${formatCount(job.failures_in_window)} of ${formatCount(job.runs_in_window)} runs (${share}) failed in this window.`,
    }
  }
  return {
    label: 'Healthy',
    tone: 'ok',
    detail: `Every one of ${formatCount(job.runs_in_window)} runs in this window completed.`,
  }
}

/* ---------------------------------------------------------- the decisions */

export const DECISIONS_INTRO =
  'An assignment records where a mailbox ended up and never why. This is the missing half: every automated placement decision, in the words it was recorded with.'

export const DECISIONS_PICK_PROMPT = 'Pick a mailbox to see why it is on the worker it is on.'

export const DECISIONS_EMPTY_TITLE = 'No decision recorded for this mailbox'

export const DECISIONS_EMPTY_DESCRIPTION =
  'A mailbox is placed the first time it sends, so one that has never sent has no placement history. Decisions are also kept for 90 days, so an older one may have been purged.'

export const DECISION_KIND_COPY: Record<string, { label: string; detail: string }> = {
  assign: { label: 'Placed', detail: 'The mailbox was given a worker.' },
  rotate: { label: 'Moved', detail: 'The mailbox was moved from one worker to another.' },
  quarantine: { label: 'Quarantined', detail: 'A worker was taken out of service for new placements.' },
  refused: {
    label: 'Refused',
    detail: 'A placement was declined. The mailbox did not get a worker, and before this log that was invisible.',
  },
}

/**
 * `kind` is a closed set in the contract today, but `triggered_by` is explicitly
 * open and new automated actors add values. An unrecognised kind is shown as it
 * arrived rather than folded into one this build happens to know.
 */
export function decisionKindCopy(kind: string): { label: string; detail: string } {
  return (
    DECISION_KIND_COPY[kind] ?? {
      label: kind,
      detail: `This build has no reading for the decision kind "${kind}". It is named as it arrived.`,
    }
  )
}

/**
 * Who decided, in words. "auto:assign" and "operator:<uuid>" are both correct
 * and neither is readable; the user id is not resolved to a name because this
 * screen has no user directory and a raw UUID beside "operator" says the
 * necessary part — a person did this, not the system.
 */
export function triggeredByLabel(triggeredBy: string): string {
  if (triggeredBy.startsWith('operator:')) return 'by an operator'
  if (triggeredBy.startsWith('auto:')) return 'automatically'
  return `by ${triggeredBy}`
}

/**
 * How the worker's id was derived, and what that means for how much to trust it
 * as a historical reference.
 *
 * This is the one place the screen explains why two rows naming different
 * worker ids might be the same machine.
 */
const ID_FAMILY_DETAIL: Record<string, string> = {
  ipv4: 'Identified by its IPv4 address, so this id is stable for as long as the address is.',
  ipv6: 'Identified by its IPv6 address, so this id is stable for as long as the address is.',
  hostname:
    'Identified by its hostname, which usually changes when the container is replaced — so an older decision naming a different id may have been this same machine.',
  override: 'Identified by an id set explicitly in this deployment’s configuration.',
}

export function idFamilyDetail(family: string): string {
  return ID_FAMILY_DETAIL[family] ?? `Identified by "${family}", which this build has no reading for.`
}

/* ------------------------------------------------------------- the errors */

/**
 * A failed read of any part of the fleet view.
 *
 * The danger specific to this screen: an empty fleet is a perfectly ordinary
 * state, so a failed request rendered as "nothing is pinned" would tell an
 * operator their mail is fine when nothing at all is known. Every branch reads
 * as a request that failed.
 *
 * 403 gets its own sentence naming the role, because on this surface it is the
 * expected answer for most of the workspace rather than a misconfiguration.
 */
export function fleetErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so the state of the fleet is unknown right now. Check your connection and try again.'
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return 'Reading the fleet needs workspace admin. This account does not have it.'
    case 400:
      return 'That mailbox reference is not one the server recognises. Pick a mailbox from the list.'
    default:
      return serverDetail(error) ?? fallback
  }
}
