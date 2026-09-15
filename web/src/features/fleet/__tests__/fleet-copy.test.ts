import { expect, test } from 'vitest'
import type { FleetProviderSignals, ScheduledJob } from '../api'
import {
  formatDuration,
  formatPercent,
  jobStatusCopy,
  percentOf,
  providerLabel,
  signalSeverity,
  signalSummary,
  triggeredByLabel,
} from '../fleet-copy'

function signals(overrides: Partial<FleetProviderSignals> = {}): FleetProviderSignals {
  return {
    provider: 'smtp',
    operation: 'send',
    attempts: 0,
    successes: 0,
    auth_failures: 0,
    throttled: 0,
    blocked: 0,
    rejected: 0,
    ...overrides,
  }
}

function job(overrides: Partial<ScheduledJob> = {}): ScheduledJob {
  return {
    job_name: 'domain auth sweep',
    last_started_at: new Date(Date.now() - 60_000).toISOString(),
    last_finished_at: new Date(Date.now() - 59_000).toISOString(),
    last_duration_ms: 1000,
    last_outcome: 'ok',
    runs_in_window: 12,
    failures_in_window: 0,
    last_failure_at: null,
    ...overrides,
  }
}

// A percentage of nothing is not zero percent. Returning 0 here would render
// "0%" beside "0 attempts", which reads as total failure when it means silence.
test('a share of nothing is null rather than zero', () => {
  expect(percentOf(0, 0)).toBeNull()
  expect(percentOf(5, 0)).toBeNull()
  expect(percentOf(0, 100)).toBe(0)
})

test('a null share renders as an em dash, never as 0%', () => {
  expect(formatPercent(null)).toBe('—')
  expect(formatPercent(0)).toBe('0%')
})

// Below 10% keeps a decimal because the difference between 0.4% and 1.2% auth
// failures is the whole signal; above it the decimal is noise.
test('percentages keep precision exactly where it carries information', () => {
  expect(formatPercent(1.24)).toBe('1.2%')
  expect(formatPercent(91.4)).toBe('91%')
  // A real but tiny rate must not round to 0% — that would report a problem as
  // its absence.
  expect(formatPercent(0.04)).toBe('<0.1%')
})

test('a sub-second sweep keeps its milliseconds rather than rounding to 0s', () => {
  expect(formatDuration(420)).toBe('420ms')
  expect(formatDuration(1500)).toBe('1.5s')
  expect(formatDuration(95_000)).toBe('1m 35s')
})

test('m365 is named as a person would say it', () => {
  expect(providerLabel('m365')).toBe('Microsoft 365')
  // An unknown provider is shown as it arrived rather than folded into one this
  // build happens to know.
  expect(providerLabel('carrier-pigeon')).toBe('carrier-pigeon')
})

// THE CENTRAL JUDGEMENT ON THIS SCREEN. Severity must come from what the
// provider thinks of the ADDRESS, never from the success rate — a worker whose
// mailboxes are sending to dead addresses has a poor success rate and is
// perfectly healthy, and treating that as sickness is the misreading the
// contract explicitly warns about.
test('severity ignores recipient rejections and reacts to credential refusals', () => {
  // 100 attempts, 90 of them permanently rejected by recipients: a 10% success
  // rate, and nothing at all wrong with the worker.
  expect(signalSeverity(signals({ attempts: 100, successes: 10, rejected: 90 }))).toBe('healthy')
  // The same volume, one credential refusal: that IS about the worker.
  expect(signalSeverity(signals({ attempts: 100, successes: 99, auth_failures: 1 }))).toBe('watch')
  expect(signalSeverity(signals({ attempts: 100, successes: 94, auth_failures: 6 }))).toBe('bad')
  // A single hard refusal of the address outweighs any amount of success.
  expect(signalSeverity(signals({ attempts: 1000, successes: 999, blocked: 1 }))).toBe('bad')
  // Throttling is the early warning, not yet a failure.
  expect(signalSeverity(signals({ attempts: 100, successes: 100, throttled: 4 }))).toBe('watch')
})

// Silence is its own state. A worker that reported nothing must never be
// classified with the healthy ones, because "nothing arrived" and "everything
// succeeded" have opposite implications.
test('a worker that reported nothing is quiet, not healthy', () => {
  expect(signalSeverity(signals())).toBe('quiet')
  expect(signalSummary(signals())).toMatch(/silence, not success/)
})

// Every summary must name the number it is about: "degraded" alone is not
// something an operator can act on.
test('each summary names the count it is reporting', () => {
  expect(signalSummary(signals({ attempts: 50, successes: 40, auth_failures: 10 }))).toContain('10 of 50')
  expect(signalSummary(signals({ attempts: 50, successes: 49, blocked: 1 }))).toContain('1 time')
  expect(signalSummary(signals({ attempts: 50, successes: 48, throttled: 2 }))).toContain('2 times')
})

// THE ORDERING BUG THIS TEST EXISTS FOR. A sweep that silently stopped still
// reports its last outcome as whatever it was when it stopped — usually 'ok'.
// Checking the outcome before checking whether it ran at all paints the worst
// case green.
test('a sweep that stopped running is reported as stopped even though its last run succeeded', () => {
  const stopped = jobStatusCopy(job({ last_outcome: 'ok', runs_in_window: 0, failures_in_window: 0 }))
  expect(stopped.label).toBe('Not running')
  expect(stopped.tone).toBe('bad')
})

test('a failing sweep reports its failure share, and a recovered one is distinguished from it', () => {
  const failing = jobStatusCopy(job({ last_outcome: 'error', runs_in_window: 100, failures_in_window: 12 }))
  expect(failing.label).toBe('Failing')
  expect(failing.detail).toContain('12 of 100')

  // Last run succeeded but earlier ones did not: neither healthy nor failing.
  const recovering = jobStatusCopy(job({ last_outcome: 'ok', runs_in_window: 100, failures_in_window: 12 }))
  expect(recovering.label).toBe('Recovering')
  expect(recovering.tone).toBe('warn')
})

test('a healthy sweep says how many runs it is vouching for', () => {
  expect(jobStatusCopy(job({ runs_in_window: 288 })).detail).toContain('288')
})

test('an operator decision is distinguished from an automatic one', () => {
  expect(triggeredByLabel('operator:9f1c')).toBe('by an operator')
  expect(triggeredByLabel('auto:assign')).toBe('automatically')
  // The vocabulary is open: an unrecognised actor is named rather than dropped.
  expect(triggeredByLabel('auto-pilot')).toBe('by auto-pilot')
})
