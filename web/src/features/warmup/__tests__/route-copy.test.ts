import { expect, test } from 'vitest'
import { routesReading, type RouteRate, type RouteReading, type WarmupRoute } from '../route-copy'

function route(overrides: Partial<WarmupRoute> = {}): WarmupRoute {
  return {
    destination_esp: 'google',
    placement_sample_7d: 120,
    inbox_rate_7d: 0.98,
    spam_rate_7d: 0.02,
    tabbed_rate_7d: 0.1,
    tab_capable_sample_7d: 60,
    ...overrides,
  }
}

/** The rows, or a failure that says the reading collapsed to the wrong branch. */
function rowsOf(...routes: WarmupRoute[]): RouteReading[] {
  const reading = routesReading(routes)
  if (reading.kind !== 'observed') throw new Error(`expected observed routes, got ${reading.kind}`)
  return reading.routes
}

function only(...routes: WarmupRoute[]): RouteReading {
  const rows = rowsOf(...routes)
  const first = rows[0]
  if (!first) throw new Error('no route was read')
  return first
}

function rateOf(row: RouteReading, label: string): RouteRate {
  const rate = row.rates.find((candidate) => candidate.label === label)
  if (!rate) throw new Error(`no ${label} rate on the ${row.destination} route`)
  return rate
}

function soleNoteOf(...routes: WarmupRoute[]): string | null {
  const reading = routesReading(routes)
  if (reading.kind !== 'observed') throw new Error(`expected observed routes, got ${reading.kind}`)
  return reading.soleNote
}

/* ------------------------------------------------------------ destinations */

test('every destination is named in words, and the contract token never survives', () => {
  const rows = rowsOf(
    route({ destination_esp: 'google' }),
    route({ destination_esp: 'microsoft' }),
    route({ destination_esp: 'other' }),
    route({ destination_esp: 'unknown' }),
  )

  expect(rows.map((row) => row.destination)).toEqual([
    'Google',
    'Microsoft',
    'Another provider',
    'Destination not resolved',
  ])
  for (const row of rows) {
    // Case-sensitive and word-bounded: the lower-case contract token is the thing
    // that must never reach a screen, while "Another provider" legitimately
    // contains the letters of `other`.
    expect(row.destination).not.toMatch(/\b(google|microsoft|other|unknown)\b/)
  }
})

// THE distinction of this module, alongside the single-route note. `other` is the
// receiver's own identity — resolved, and neither Google nor Microsoft.
// `unknown` is our lookup not having happened. Rendering them alike tells an
// operator we know where mail went when we do not, and both readings look
// entirely plausible in review.
test('an unresolved destination does not read like a resolved-but-unnamed one', () => {
  const rows = rowsOf(route({ destination_esp: 'other' }), route({ destination_esp: 'unknown' }))
  const [other, unknown] = rows
  if (!other || !unknown) throw new Error('both routes must be read')

  expect(other.destination).not.toBe(unknown.destination)
  expect(other.resolved).toBe(true)
  expect(unknown.resolved).toBe(false)
  // And each says which of the two facts it is.
  expect(other.destinationDetail).toMatch(/resolved, and neither Google nor Microsoft/i)
  expect(unknown.destinationDetail).toMatch(/has not been resolved yet/i)
})

// `unknown` is not a fourth provider. Its label must not name a place, or it sits
// beside Google and Microsoft as though mail was delivered somewhere called
// "unknown".
test('an unresolved destination is labelled as unresolved, never as a provider', () => {
  const unknown = only(route({ destination_esp: 'unknown' }))

  expect(unknown.destination).toMatch(/not resolved/i)
  expect(unknown.destination).not.toMatch(/provider|google|microsoft/i)
})

// A destination the backend adds before this build learns it. Folding it into
// `unknown` would report a resolved destination as an unresolved one; folding it
// into `other` would claim we know it is neither Google nor Microsoft.
test('a destination this build does not know is shown as it arrived', () => {
  const exotic = only(route({ destination_esp: 'zoho' as WarmupRoute['destination_esp'] }))

  expect(exotic.destination).toMatch(/zoho/)
  expect(exotic.destination).toMatch(/does not know/i)
  expect(exotic.destination).not.toMatch(/not resolved/i)
  expect(exotic.resolved).toBe(true)
})

/* ------------------------------------------------------------------- rates */

// The split makes every denominator smaller, so a route below the sample floor is
// ordinary. It arrives as null and must read as "no rate yet", never as a clean
// zero — a false-clean row on the one destination with too little evidence is
// exactly the reading an operator would act on.
test('a null rate is not established, and never a percentage', () => {
  const sparse = only(route({ placement_sample_7d: 4, inbox_rate_7d: null, spam_rate_7d: null }))

  for (const label of ['Inbox 7d', 'Spam 7d']) {
    const rate = rateOf(sparse, label)
    expect(rate.value).toBe('Not established')
    expect(rate.measured).toBe(false)
    expect(rate.value).not.toMatch(/%/)
    expect(rate.detail).toMatch(/not a zero/i)
  }
})

// The inverse case, and the one a "falsy means unknown" implementation gets
// wrong: zero over a real sample is a measurement — no mail on this route went to
// spam — and reporting it as "not established" throws away the only good news
// this matrix can deliver.
test('a measured zero is a measurement, not an absence', () => {
  const clean = only(route({ placement_sample_7d: 120, spam_rate_7d: 0, tabbed_rate_7d: 0 }))

  const spam = rateOf(clean, 'Spam 7d')
  expect(spam.value).toBe('0%')
  expect(spam.measured).toBe(true)
  expect(spam.value).not.toMatch(/not established/i)

  const tabbed = rateOf(clean, 'Tabbed 7d')
  expect(tabbed.value).toBe('0%')
  expect(tabbed.measured).toBe(true)
})

// A rate that rounds to nothing is still a signal. "0%" would say the opposite of
// what was observed.
test('a positive rate that rounds to nothing reads as under one percent', () => {
  const trace = only(route({ spam_rate_7d: 0.002 }))

  expect(rateOf(trace, 'Spam 7d').value).toBe('<1%')
})

// Every figure is computed over its own route's count. Two routes' percentages
// mean nothing side by side until both denominators are on screen — a 50% spam
// rate over 6 observations and a 2% one over 400 are not comparable, and the
// matrix invites exactly that comparison.
test('each rate carries its own route sample, never the pooled total', () => {
  const rows = rowsOf(
    route({ destination_esp: 'google', placement_sample_7d: 400, tab_capable_sample_7d: 300 }),
    route({ destination_esp: 'microsoft', placement_sample_7d: 6, tab_capable_sample_7d: 0, tabbed_rate_7d: null }),
  )
  const [google, microsoft] = rows
  if (!google || !microsoft) throw new Error('both routes must be read')

  expect(rateOf(google, 'Inbox 7d').population).toBe('of 400 observations on this route')
  expect(rateOf(google, 'Spam 7d').population).toBe('of 400 observations on this route')
  expect(rateOf(google, 'Tabbed 7d').population).toBe('of 300 tab-capable on this route')

  expect(rateOf(microsoft, 'Inbox 7d').population).toBe('of 6 observations on this route')
  expect(rateOf(microsoft, 'Spam 7d').population).toBe('of 6 observations on this route')
})

// Tabs are undetectable to a reader that has no concept of them, so a route with
// no tab-capable observation measured nothing — it did not measure a clean
// primary inbox.
test('a route nothing could categorise says so rather than reporting a clean tab rate', () => {
  const blind = only(route({ tabbed_rate_7d: null, tab_capable_sample_7d: 0 }))

  const tabbed = rateOf(blind, 'Tabbed 7d')
  expect(tabbed.value).toBe('Not detectable')
  expect(tabbed.measured).toBe(false)
  expect(tabbed.population).toMatch(/no tab-capable observations/i)
  expect(tabbed.detail).toMatch(/not a clean primary-inbox result/i)
})

// A row claiming a destination but no observations is a contradiction. Saying so
// beats dividing by a population of nothing.
test('a route with no observations reports the absence, not a rate', () => {
  const empty = only(route({ placement_sample_7d: 0, inbox_rate_7d: null, spam_rate_7d: null }))

  expect(rateOf(empty, 'Inbox 7d').value).toBe('No observations')
  expect(rateOf(empty, 'Inbox 7d').detail).toMatch(/an unmeasured route is not a clean one/i)
})

test('a single observation is counted in the singular', () => {
  const one = only(route({ placement_sample_7d: 1, inbox_rate_7d: null, spam_rate_7d: null }))

  expect(rateOf(one, 'Inbox 7d').population).toBe('over 1 observation on this route')
})

/* --------------------------------------------------------- the single route */

// The worst misreading this feature enables, and design §3's explicit
// requirement. Warmup partners are the workspace's own mailboxes, so an
// all-Google pool can only ever be measured against Google — and a clean one-row
// matrix would tell its operator that Microsoft delivery is healthy when no
// warmup mail was sent to Microsoft at all.
test('one observed route says so, and says it measures nothing about the others', () => {
  const note = soleNoteOf(route({ destination_esp: 'google' }))

  expect(note).toMatch(/only one destination observed/i)
  expect(note).toMatch(/says nothing about how it is delivered to any other provider/i)
  expect(note).toMatch(/one clean row is not a clean matrix/i)
  // And it names why, so the limitation is understood rather than merely warned about.
  expect(note).toMatch(/your own connected mailboxes/i)
})

test('the sole-destination note names the destination that was observed', () => {
  expect(soleNoteOf(route({ destination_esp: 'microsoft' }))).toMatch(/only one destination observed: Microsoft\./i)
  expect(soleNoteOf(route({ destination_esp: 'other' }))).toMatch(/neither Google nor Microsoft/)
})

// §8's degradation: the sweep is behind, so the one row records no destination at
// all. Naming a provider here would invent one; the note has to say that nothing
// is known about any of them.
test('one unresolved route says nothing is known about any provider', () => {
  const note = soleNoteOf(route({ destination_esp: 'unknown' }))

  expect(note).toMatch(/only one destination observed/i)
  expect(note).toMatch(/it is not resolved/i)
  expect(note).toMatch(/nothing about delivery to Google, to Microsoft, or to anywhere else/i)
})

// Two destinations is a real matrix and stands on its own. Warning about a
// single-route pool that isn't one would train an operator to skip the note in
// the case where it matters.
test('more than one route carries no single-route warning', () => {
  expect(soleNoteOf(route({ destination_esp: 'google' }), route({ destination_esp: 'microsoft' }))).toBeNull()
})

/* ---------------------------------------------------------- nothing at all */

// An empty array is not four clean routes, and it is not a failure either.
test('no observed routes reads as nothing observed', () => {
  const reading = routesReading([])

  expect(reading.kind).toBe('unobserved')
  if (reading.kind !== 'unobserved') throw new Error('unreachable')
  expect(reading.message).toMatch(/no route to report/i)
  expect(reading.message).toMatch(/not a delivery failure/i)
})

// A server too old to report routes at all. Same absence, and never a table of
// headings with no rows.
test('a server that does not report routes reads as nothing observed', () => {
  expect(routesReading(undefined).kind).toBe('unobserved')
})

/** The sole-destination note, or null. */
function noteOf(...routes: WarmupRoute[]): string | null {
  const reading = routesReading(routes)
  if (reading.kind !== 'observed') throw new Error(`expected observed routes, got ${reading.kind}`)
  return reading.soleNote
}

// The note counts RESOLVED destinations, not rows.
//
// An unresolved row names no provider, so {Google, unresolved} confirms exactly
// one provider — the same fact {Google} alone carries. Keying the note on row
// count meant the two-row shape silently lost the warning while looking MORE
// informative than the one-row shape it is equivalent to, which is the worse of
// the two failures: an operator who sees two rows believes they are comparing
// destinations.
test('one resolved destination beside an unresolved row still warns', () => {
  const note = noteOf(route({ destination_esp: 'google' }), route({ destination_esp: 'unknown' }))

  expect(note).toMatch(/only one destination resolved/i)
  expect(note).toMatch(/Google/)
  // And it must not claim only one destination was OBSERVED — two rows were.
  expect(note).not.toMatch(/only one destination observed/i)
})

test('two resolved destinations are a real matrix and carry no note', () => {
  expect(noteOf(route({ destination_esp: 'google' }), route({ destination_esp: 'microsoft' }))).toBeNull()
})

// The unresolved row must not suppress the note by padding the count to two.
test('two resolved destinations plus an unresolved row still carry no note', () => {
  const note = noteOf(
    route({ destination_esp: 'google' }),
    route({ destination_esp: 'microsoft' }),
    route({ destination_esp: 'unknown' }),
  )
  expect(note).toBeNull()
})

// `other` is a resolved destination — checked, and it is neither Google nor
// Microsoft — so it counts toward the total the same way the named two do.
test('other counts as a resolved destination', () => {
  expect(noteOf(route({ destination_esp: 'google' }), route({ destination_esp: 'other' }))).toBeNull()
  expect(noteOf(route({ destination_esp: 'other' }), route({ destination_esp: 'unknown' })))
    .toMatch(/only one destination resolved/i)
})

test('a sole unresolved row says nothing was resolved at all', () => {
  const note = noteOf(route({ destination_esp: 'unknown' }))
  expect(note).toMatch(/not resolved/i)
  expect(note).not.toMatch(/only one destination resolved/i)
})
