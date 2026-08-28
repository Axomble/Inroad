import { expect, test } from 'vitest'
import {
  contentVersionsReading,
  VERSIONS_SOLE_NOTE,
  type ContentVersion,
} from '../content-version-copy'

/**
 * A row whose arithmetic adds up: placement_sample is inbox+spam, and the rates are
 * those counts over that sample. A fixture whose numbers disagree tests a rendering
 * the backend cannot produce.
 */
function version(overrides: Partial<ContentVersion> = {}): ContentVersion {
  return {
    version: 'sl1:aaaaaaaaaaaaaaaa',
    inbox: 40,
    spam: 10,
    placement_sample: 50,
    inbox_rate: 0.8,
    spam_rate: 0.2,
    ...overrides,
  }
}

function observed(versions: ContentVersion[]) {
  const reading = contentVersionsReading(versions)
  if (reading.kind !== 'observed') throw new Error(`expected an observed reading, got ${reading.kind}`)
  return reading
}

/* ------------------------------------------------ the three empty-ish answers */

// Undefined and [] are different answers. Collapsing them is the mistake the
// observers and incidents panels each had to be corrected for.
test('an unpublished split is not an observed-nothing one', () => {
  expect(contentVersionsReading(undefined).kind).toBe('unreported')
  expect(contentVersionsReading([]).kind).toBe('unobserved')
})

test('the unobserved answer says nothing landed, not that the library is clean', () => {
  const reading = contentVersionsReading([])
  if (reading.kind !== 'unobserved') throw new Error('expected unobserved')

  expect(reading.message).toMatch(/nothing to split by template/i)
  // It must not read as a delivery failure either — the mail may simply not have
  // been polled yet.
  expect(reading.message).toMatch(/not a delivery failure/i)
})

/* ------------------------------------------------------- null is not a zero */

// The fifth application of this rule in the subsystem. A template below the floor
// arrives with null rates and its counts intact, and the counts must survive: the
// fold withholds the RATES, not the evidence behind them.
test('a rate below the sample floor reads as not established, never 0%', () => {
  const reading = observed([version({ inbox: 4, spam: 1, placement_sample: 5, inbox_rate: null, spam_rate: null })])
  const [row] = reading.versions
  if (!row) throw new Error('expected a row')

  for (const figure of row.figures) {
    expect(figure.value).toBe('Not established')
    expect(figure.measured).toBe(false)
    expect(figure.value).not.toMatch(/0%/)
    // The denominator is still stated: an unestablished rate over 5 observations
    // is a different claim from one over 500.
    expect(figure.population).toMatch(/5 observations/)
  }
  expect(row.counts).toMatch(/4 inbox, 1 spam over 5 observations/)
})

// The opposite case, and the one a falsiness check would break: a measured zero is
// a real measurement and must not be reported as missing evidence.
test('a measured zero stays a measurement', () => {
  const reading = observed([version({ inbox: 50, spam: 0, placement_sample: 50, inbox_rate: 1, spam_rate: 0 })])
  const spam = reading.versions[0]?.figures.find((f) => f.label === 'Spam 7d')
  if (!spam) throw new Error('expected a spam figure')

  expect(spam.value).toBe('0%')
  expect(spam.measured).toBe(true)
})

// A real signal rounded down to a confident zero is the false-clean reading this
// screen keeps having to remove.
test('a positive rate that rounds to nothing reads as <1%', () => {
  const reading = observed([version({ placement_sample: 1000, spam_rate: 0.001 })])
  const spam = reading.versions[0]?.figures.find((f) => f.label === 'Spam 7d')

  expect(spam?.value).toBe('<1%')
})

test('a template nobody observed reports no observations rather than a rate', () => {
  const reading = observed([version({ inbox: 0, spam: 0, placement_sample: 0, inbox_rate: null, spam_rate: null })])
  const [row] = reading.versions

  for (const figure of row?.figures ?? []) {
    expect(figure.value).toBe('No observations')
    expect(figure.measured).toBe(false)
    expect(figure.detail).toMatch(/not a clean one/i)
  }
})

/* ------------------------------------------------- one row is not a comparison */

// The panel's value is the disparity BETWEEN templates. A single tidy row invites
// reading its rate as the library's verdict when nothing was compared to anything.
test('a sole template says so, and two templates do not', () => {
  expect(observed([version()]).soleNote).toBe(VERSIONS_SOLE_NOTE)
  expect(observed([version(), version({ version: 'sl1:bbbbbbbbbbbbbbbb' })]).soleNote).toBeNull()
})

test('the sole note explains what a second template would separate', () => {
  expect(VERSIONS_SOLE_NOTE).toMatch(/nothing to compare/i)
  expect(VERSIONS_SOLE_NOTE).toMatch(/content or the mailboxes/i)
})

/* --------------------------------------------------------------- the ordering */

// The API orders by fingerprint, which is alphabetical over a hash and carries no
// meaning, so the panel sorts by evidence instead.
test('rows are ordered by sample size, best-evidenced first', () => {
  const reading = observed([
    version({ version: 'sl1:aaaaaaaaaaaaaaaa', placement_sample: 5, inbox_rate: null, spam_rate: null }),
    version({ version: 'sl1:zzzzzzzzzzzzzzzz', placement_sample: 400 }),
    version({ version: 'sl1:mmmmmmmmmmmmmmmm', placement_sample: 50 }),
  ])

  expect(reading.versions.map((v) => v.fingerprint)).toEqual([
    'sl1:zzzzzzzzzzzzzzzz',
    'sl1:mmmmmmmmmmmmmmmm',
    'sl1:aaaaaaaaaaaaaaaa',
  ])
})

// Sorting worst-first would float the flimsiest rows to the top — a single spam
// observation reads as 100% — and present the badness verdict the confound cannot
// support.
test('a thin 100%-spam template does not outrank a well-evidenced one', () => {
  const reading = observed([
    version({ version: 'sl1:thin', inbox: 0, spam: 1, placement_sample: 1, inbox_rate: null, spam_rate: null }),
    version({ version: 'sl1:solid', inbox: 300, spam: 100, placement_sample: 400, inbox_rate: 0.75, spam_rate: 0.25 }),
  ])

  expect(reading.versions[0]?.fingerprint).toBe('sl1:solid')
})

// Equal evidence must hold still between renders rather than swapping on each poll.
test('equal-sample rows fall back to a stable fingerprint order', () => {
  const rows = [
    version({ version: 'sl1:bbbb', placement_sample: 50 }),
    version({ version: 'sl1:aaaa', placement_sample: 50 }),
  ]

  expect(observed(rows).versions.map((v) => v.fingerprint)).toEqual(['sl1:aaaa', 'sl1:bbbb'])
  expect(observed([...rows].reverse()).versions.map((v) => v.fingerprint)).toEqual(['sl1:aaaa', 'sl1:bbbb'])
})

// The caller's array is the RTK Query cache's; sorting it in place would mutate
// frozen state and reorder every other reader of the same object.
test('the reading does not reorder the array it was given', () => {
  const input = [version({ version: 'sl1:aaaa', placement_sample: 5 }), version({ version: 'sl1:zzzz', placement_sample: 400 })]

  contentVersionsReading(input)

  expect(input.map((v) => v.version)).toEqual(['sl1:aaaa', 'sl1:zzzz'])
})

/* ------------------------------------------------------------ the fingerprint */

// The fingerprint is the only handle for matching a row against the library, so it
// is shortened for display and kept in full beside it — never renamed.
test('a long fingerprint is shortened for display and kept whole', () => {
  const [row] = observed([version({ version: 'sl1:aaaaaaaaaaaaaaaa' })]).versions

  expect(row?.label).toBe('sl1:aaaaaaaa…')
  expect(row?.fingerprint).toBe('sl1:aaaaaaaaaaaaaaaa')
  expect(row?.label).not.toMatch(/template 1/i)
})

test('a short fingerprint is left alone rather than padded or elided', () => {
  const [row] = observed([version({ version: 'sl1:aaaa' })]).versions

  expect(row?.label).toBe('sl1:aaaa')
  expect(row?.label).not.toMatch(/…/)
})

// The JSON boundary is not the backend's closed vocabulary. A blank fingerprint is
// a malformed row, not a template whose name happens to be empty.
test('a row with no fingerprint is shown as unidentified, and two stay distinct', () => {
  const reading = observed([
    version({ version: '', placement_sample: 10 }),
    version({ version: '   ', placement_sample: 9 }),
  ])

  for (const row of reading.versions) {
    expect(row.label).toMatch(/not identified/i)
  }
  expect(reading.versions[0]?.key).not.toBe(reading.versions[1]?.key)
})
