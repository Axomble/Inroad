import { expect, test } from 'vitest'

// The pool floor the API serves. Stated here rather than imported so these tests
// pin the behaviour AT a threshold instead of agreeing with whatever the source
// currently says — an assertion that reads its expectation from the code under
// test cannot fail when that code changes.
const MIN_POOL = 4
import type { WarmupMailbox } from '@/store/api'
import {
  INCIDENTS_GATES_NOTHING,
  INCIDENTS_INTRO,
  incidentsReading,
  type IncidentReading,
  type IncidentStat,
  type IncidentsReading,
  type WarmupIncident,
} from '../incident-copy'

function incident(overrides: Partial<WarmupIncident> = {}): WarmupIncident {
  return {
    dimension: 'signing_domain',
    value: 'mail.acme.test',
    member_mailbox_ids: ['mb-1', 'mb-2', 'mb-3', 'mb-4'],
    cohort_size: 5,
    degraded_inside: 4,
    cohort_outside: 20,
    degraded_outside: 1,
    lift: 16,
    ...overrides,
  }
}

function participant(overrides: Partial<WarmupMailbox> = {}): WarmupMailbox {
  return {
    mailbox_id: 'mb-1',
    email: 'one@acme.test',
    enabled: true,
    health_state: 'healthy',
    health_reason: '',
    lane: 'healthy',
    lane_reason: '',
    today_sent: 4,
    today_target: 4,
    placement_sample_7d: 40,
    inbox_rate_7d: 0.98,
    spam_rate_7d: 0.02,
    ...overrides,
  }
}

/** A pool of healthy participants, ids `mb-1`..`mb-n` and matching emails. */
function pool(size: number, overrides: (index: number) => Partial<WarmupMailbox> = () => ({})): WarmupMailbox[] {
  return Array.from({ length: size }, (_, i) =>
    participant({ mailbox_id: `mb-${i + 1}`, email: `mb${i + 1}@acme.test`, ...overrides(i) }),
  )
}

/** The reading, or a failure naming the branch it collapsed into. */
function expectKind<K extends IncidentsReading['kind']>(
  reading: IncidentsReading,
  kind: K,
): Extract<IncidentsReading, { kind: K }> {
  if (reading.kind !== kind) throw new Error(`expected ${kind}, got ${reading.kind}`)
  return reading as Extract<IncidentsReading, { kind: K }>
}

function rowsOf(incidents: WarmupIncident[], participants = pool(25)): IncidentReading[] {
  return expectKind(incidentsReading(incidents, participants, MIN_POOL), 'detected').incidents
}

function only(one: WarmupIncident, participants = pool(25)): IncidentReading {
  const rows = rowsOf([one], participants)
  const first = rows[0]
  if (!first) throw new Error('no incident was read')
  return first
}

function statOf(row: IncidentReading, label: string): IncidentStat {
  const stat = row.stats.find((candidate) => candidate.label.toLowerCase().includes(label.toLowerCase()))
  if (!stat) throw new Error(`no ${label} figure on the ${row.dimension} row`)
  return stat
}

function messageOf(incidents: WarmupIncident[], participants: WarmupMailbox[]): string {
  const reading = incidentsReading(incidents, participants, MIN_POOL)
  if (reading.kind === 'quiet' || reading.kind === 'none-found') return reading.message
  throw new Error(`expected an empty reading, got ${reading.kind}`)
}

/* -------------------------------------------------------------- dimensions */

// Every dimension is named in the operator's own language. `signing_domain` is a
// column name; "signing domain (DKIM)" is the thing an operator has a DNS record
// for, and the parenthesis is what connects the two for someone who knows the
// record but not our column.
test('every dimension is named in operator language, and the contract token never survives', () => {
  const rows = rowsOf([
    incident({ dimension: 'destination_route', value: 'microsoft' }),
    incident({ dimension: 'signing_domain', value: 'mail.acme.test' }),
    incident({ dimension: 'return_path_domain', value: 'bounces.acme.test' }),
    incident({ dimension: 'sender_domain', value: 'acme.test' }),
  ], pool(25))

  expect(rows.map((row) => row.dimension)).toEqual([
    'destination',
    'signing domain (DKIM)',
    'return path',
    'sender domain',
  ])
  for (const row of rows) {
    expect(row.dimension).not.toMatch(/_/)
  }
})

// Four rows, four sentences: what the mailboxes share, said in the terms of that
// dimension. A single generic sentence would make "they share a destination" and
// "they share a DKIM key" the same finding, which is the collapse the whole
// dimension split exists to avoid.
test('each dimension says what the mailboxes actually share', () => {
  const route = only(incident({ dimension: 'destination_route', value: 'microsoft' }))
  const signing = only(incident({ dimension: 'signing_domain' }))
  const returnPath = only(incident({ dimension: 'return_path_domain', value: 'bounces.acme.test' }))
  const sender = only(incident({ dimension: 'sender_domain', value: 'acme.test' }))

  expect(route.dimensionDetail).toMatch(/where their mail went/i)
  expect(route.dimensionDetail).toMatch(/MX/)
  expect(signing.dimensionDetail).toMatch(/signed by the same DKIM d= domain/i)
  expect(returnPath.dimensionDetail).toMatch(/bounces for their mail go to the same host/i)
  expect(sender.dimensionDetail).toMatch(/same organizational domain/i)

  // And no two dimensions borrow each other's sentence.
  const details = [route, signing, returnPath, sender].map((row) => row.dimensionDetail)
  expect(new Set(details).size).toBe(4)
})

// A dimension the backend adds before this build learns it. Folding it into one
// of the four would attribute the correlation to a dimension nobody reported.
test('a dimension this build does not know is named as it arrived', () => {
  const exotic = only(incident({ dimension: 'observed_asn' as WarmupIncident['dimension'], value: 'AS15169' }))

  expect(exotic.dimension).toMatch(/observed_asn/)
  expect(exotic.dimension).toMatch(/does not know/i)
  expect(exotic.dimensionDetail).toMatch(/no reading for that dimension/i)
})

// A destination is the one dimension whose value is a contract token rather than
// data. `microsoft` is our word for a provider, and the route matrix already
// refuses to put it on a screen — the same provider must not be "Microsoft" in
// one panel and `microsoft` in another.
test('a destination incident names the provider the way the route matrix does', () => {
  const named = only(incident({ dimension: 'destination_route', value: 'microsoft' }))
  const resolvedButUnnamed = only(incident({ dimension: 'destination_route', value: 'other' }))

  expect(named.value).toBe('Microsoft')
  expect(resolvedButUnnamed.value).toBe('Another provider')

  // And a domain is data: it is shown exactly as it was recorded.
  expect(only(incident({ dimension: 'signing_domain', value: 'mail.acme.test' })).value).toBe('mail.acme.test')
})

/* ------------------------------------------------------------- the arithmetic */

// §8's rule: show the arithmetic, not a verdict. Both counts, because neither
// means anything alone — 4 of 5 degraded is a finding only while the rest of the
// pool is 1 of 20, and nothing at all while the rest is 18 of 20.
test('both sides of the comparison are shown, each over its own population', () => {
  const row = only(incident({ cohort_size: 5, degraded_inside: 4, cohort_outside: 20, degraded_outside: 1 }))

  expect(statOf(row, 'those sharing it').value).toBe('4 of 5')
  expect(statOf(row, 'rest of the pool').value).toBe('1 of 20')
  // The value it is all about, and it is the value itself — not a summary of it.
  expect(row.value).toBe('mail.acme.test')
})

// A lift of 2.1 and a lift of 12 are very different findings, and a badge that
// says "incident" hides the difference. Both figures reach the screen, and the
// marginal one says plainly that it may be chance.
test('a marginal concentration and a strong one do not read alike', () => {
  const marginal = statOf(only(incident({ lift: 2.1 })), 'concentration')
  const strong = statOf(only(incident({ lift: 12.4 })), 'concentration')

  expect(marginal.value).toBe('2.1×')
  expect(strong.value).toBe('12×')

  // Both explain what the figure is against, so neither is a bare multiplier.
  expect(marginal.detail).toMatch(/1× would be no concentration at all/i)
  expect(strong.detail).toMatch(/1× would be no concentration at all/i)

  // Only the marginal one is hedged: hedging a 12× would train an operator to
  // discount every row, and hedging neither would make 2.1× look like 12×.
  expect(marginal.detail).toMatch(/read it as a hint/i)
  expect(marginal.detail).toMatch(/by chance/i)
  expect(strong.detail).not.toMatch(/hint|by chance/i)
})

// A figure that cannot be printed is not printed. `NaN×` on a screen is a
// rendering bug that reads as a finding, and the two counts carry the row.
test('an unusable concentration figure says so rather than rendering as a number', () => {
  const stat = statOf(only(incident({ lift: Number.NaN })), 'concentration')

  expect(stat.value).toBe('Not stated')
  expect(stat.value).not.toMatch(/NaN|Infinity|×/)
  expect(stat.detail).toMatch(/not a zero, and not a strong result/i)
})

/* ---------------------------------------------------------------- the members */

// The degraded members are named, because "4 mailboxes" leaves an operator to
// diff the pool by hand — the exact work this feature removes.
test('the degraded members are named by email', () => {
  const row = only(
    incident({ member_mailbox_ids: ['mb-2', 'mb-3'], degraded_inside: 2, cohort_size: 3 }),
    pool(25),
  )

  expect(row.members).toEqual(['mb2@acme.test', 'mb3@acme.test'])
})

// A member the pool cannot name is still a member. Dropping it would leave the
// list one short of the count beside it, which reads as a rendering that lost a
// mailbox rather than as a pool that has one we cannot name.
test('a member the pool cannot name is shown as its id, never dropped', () => {
  const row = only(
    incident({ member_mailbox_ids: ['mb-1', 'mb-404'], degraded_inside: 2, cohort_size: 3 }),
    pool(25),
  )

  expect(row.members).toEqual(['mb1@acme.test', 'mb-404'])
  // Two named, and the count beside them says two. A dropped member would leave
  // the list disagreeing with the arithmetic.
  expect(statOf(row, 'those sharing it').value).toBe('2 of 3')
})

/* ---------------------------------------------------------------- `unknown` */

// The rule with the sharpest edge: grouping degraded mailboxes on a value that
// means "we never resolved this" correlates on our own ignorance, and it fires
// hardest on the pools carrying the least data. The backend excludes it; if one
// arrives anyway it is not rendered as a fault domain.
test('an unresolved value is not a fault domain and is never rendered as one', () => {
  const degraded = pool(6, (i) => (i < 3 ? { health_state: 'paused' } : {}))

  for (const value of ['unknown', 'UNKNOWN', '', '  ']) {
    const reading = incidentsReading([incident({ value })], degraded, MIN_POOL)

    // It does not become a row...
    expect(reading.kind).toBe('none-found')
    // ...and the pool then reads as what it is: degraded, with nothing shared.
    expect(expectKind(reading, 'none-found').message).not.toMatch(/unknown/i)
  }
})

// The resolved values around it still report, so the exclusion is of the value
// and not of the dimension.
test('an unresolved value does not suppress the resolved incidents beside it', () => {
  const rows = rowsOf([incident({ value: 'unknown' }), incident({ value: 'mail.acme.test' })])

  expect(rows).toHaveLength(1)
  expect(rows[0]?.value).toBe('mail.acme.test')
})

/* --------------------------------------------------- no incidents, four ways */

// Nothing is degrading. The reading an operator most needs kept apart from the
// one below it: "no shared cause found" over a healthy pool says the search came
// back empty, when there was nothing to search across.
test('a pool with nothing degrading says that, not that no cause was found', () => {
  const message = messageOf([], pool(6))

  expect(message).toMatch(/no degradation in the pool/i)
  expect(message).toMatch(/6 participants/)
  expect(message).toMatch(/nothing to correlate/i)
  expect(message).toMatch(/nothing to look across/i)
  expect(message).not.toMatch(/no shared cause found/i)
})

// The same empty array, and a different answer. This is the pair the contract
// cannot distinguish on its own: `incidents: []` is byte-identical in both cases
// and only the pool it arrived with says which sentence is true.
test('a pool with degradation and no concentration says no shared cause was found', () => {
  const degraded = pool(9, (i) => (i < 4 ? { health_state: 'throttled' } : {}))
  const message = messageOf([], degraded)

  expect(message).toMatch(/^4 mailboxes are degrading/)
  expect(message).toMatch(/no shared cause found/i)
  expect(message).toMatch(/which is an answer/i)
  // It names the four dimensions searched, so "no shared cause" is not read as a
  // claim about every possible cause.
  expect(message).toMatch(/no destination, signing domain, return path or sender domain/i)
  expect(message).not.toMatch(/no degradation in the pool/i)
})

// Both axes count as degradation, because the two are independent by design and
// a shared cause surfaces on either — a filtering relay lands on health, an
// authentication fault lands on the lane. A reading that watched only
// `health_state` would call a pool of withheld mailboxes quiet.
test('degradation on the lane axis alone still counts as degradation', () => {
  const laneOnly = pool(9, (i) => (i < 3 ? { lane: 'quarantine' } : {}))
  const recovery = pool(9, (i) => (i < 2 ? { lane: 'recovery' } : {}))

  expect(messageOf([], laneOnly)).toMatch(/^3 mailboxes are degrading/)
  expect(messageOf([], recovery)).toMatch(/^2 mailboxes are degrading/)
})

// One mailbox cannot correlate with anything, so "no shared cause found" would
// describe a search that was never possible rather than one that came back
// empty.
test('a single degrading mailbox says a pattern needs at least two', () => {
  const message = messageOf([], pool(9, (i) => (i < 1 ? { health_state: 'watch' } : {})))

  expect(message).toMatch(/one mailbox is degrading/i)
  expect(message).toMatch(/cannot correlate with anything/i)
  expect(message).toMatch(/at least two/i)
  expect(message).not.toMatch(/no shared cause found/i)
})

// Design §9: a pool too small for concentration to exist is told so, rather than
// shown a clean "none found" it had no way of earning.
test('a pool too small for concentration says so instead of reporting none found', () => {
  const message = messageOf([], pool(3, (i) => (i < 2 ? { health_state: 'paused' } : {})))

  expect(message).toMatch(/cannot show concentration at all/i)
  expect(message).toMatch(/at least 4 participants/i)
  expect(message).toMatch(/not enough pool to look/i)
  expect(message).not.toMatch(/no shared cause found/i)
})

// Four participants is the smallest pool that can report anything, so it gets the
// ordinary answer rather than the too-small one.
test('the smallest pool that can show concentration gets the ordinary answer', () => {
  const message = messageOf([], pool(4, (i) => (i < 2 ? { health_state: 'paused' } : {})))

  expect(message).toMatch(/no shared cause found/i)
  expect(message).not.toMatch(/not enough pool to look/i)
})

// A mailbox that left the pool is not a participant. Counting its last-known
// health as current degradation would report a search across mailboxes the
// backend never considered.
test('a disabled mailbox is not a participant, however it was last seen', () => {
  const message = messageOf([], pool(6, (i) => (i < 2 ? { enabled: false, health_state: 'paused' } : {})))

  expect(message).toMatch(/no degradation in the pool/i)
  // And it is not counted in the pool size either.
  expect(message).toMatch(/4 participants/)
})

// A server that does not report incidents has run no inference, and neither has
// an empty workspace. Both are silence, because every other answer here claims a
// search happened.
test('nothing is claimed when no inference was made', () => {
  expect(incidentsReading(undefined, pool(6), MIN_POOL).kind).toBe('unreported')
  expect(incidentsReading([], [], MIN_POOL).kind).toBe('unreported')
  expect(incidentsReading([incident()], [], MIN_POOL).kind).toBe('unreported')
  // A pool of nothing but disabled mailboxes is the same absence.
  expect(incidentsReading([], pool(3, () => ({ enabled: false })), MIN_POOL).kind).toBe('unreported')
})

/* -------------------------------------------------------------- truncation */

// One bad relay in a twenty-mailbox pool can report on all four dimensions at
// several values each, and a screen of those pushes the mailbox list below the
// fold. The cap is said out loud, because a silent one is a lie about how much
// was found.
test('only the strongest few are shown, and the rest are counted out loud', () => {
  const many = [
    incident({ dimension: 'signing_domain', value: 'a.test', lift: 9 }),
    incident({ dimension: 'return_path_domain', value: 'b.test', lift: 8 }),
    incident({ dimension: 'sender_domain', value: 'c.test', lift: 7 }),
    incident({ dimension: 'destination_route', value: 'microsoft', lift: 6 }),
    incident({ dimension: 'signing_domain', value: 'd.test', lift: 5 }),
    incident({ dimension: 'signing_domain', value: 'e.test', lift: 4 }),
  ]
  const reading = expectKind(incidentsReading(many, pool(25), MIN_POOL), 'detected')

  expect(reading.incidents.map((row) => row.value)).toEqual(['a.test', 'b.test', 'c.test', 'Microsoft'])
  expect(reading.truncated).toMatch(/2 weaker correlations are not shown/i)
})

// Exactly at the cap, which is the boundary and the only interesting side of it:
// one correlation per dimension is a pool with nothing hidden, and a note saying
// "0 weaker correlations are not shown" is a rendering artefact read as a
// finding. A fixture below the cap cannot tell the two apart.
test('a pool exactly at the cap says nothing about hidden correlations', () => {
  const atTheCap = [
    incident({ dimension: 'signing_domain', value: 'a.test' }),
    incident({ dimension: 'return_path_domain', value: 'b.test' }),
    incident({ dimension: 'sender_domain', value: 'c.test' }),
    incident({ dimension: 'destination_route', value: 'microsoft' }),
  ]
  const reading = expectKind(incidentsReading(atTheCap, pool(25), MIN_POOL), 'detected')

  expect(reading.incidents).toHaveLength(4)
  expect(reading.truncated).toBeNull()
})

/* ------------------------------------------------- correlation, never a cause */

// THE rule of this module. "These four mailboxes share a signing domain" is what
// the data supports; "your DKIM key is broken" is not, and every plausible
// rendering of the first states the second. Asserted over every string the panel
// can put on screen, because one sentence anywhere is enough to promote the
// finding.
test('nothing the panel can say claims a cause', () => {
  const detected = expectKind(
    incidentsReading(
      [
        incident({ dimension: 'destination_route', value: 'microsoft', lift: 2.2 }),
        incident({ dimension: 'signing_domain', lift: 14 }),
        incident({ dimension: 'return_path_domain', value: 'bounces.acme.test' }),
      ],
      pool(25),
      MIN_POOL,
    ),
    'detected',
  )
  const rows: string[] = []
  for (const row of detected.incidents) {
    rows.push(row.dimension, row.dimensionDetail)
    for (const stat of row.stats) rows.push(stat.label, stat.detail ?? '')
  }
  const everything = [
    INCIDENTS_INTRO,
    INCIDENTS_GATES_NOTHING,
    messageOf([], pool(6)),
    messageOf([], pool(9, (i) => (i < 4 ? { health_state: 'paused' } : {}))),
    ...rows,
  ].join('\n')

  // The wordings that promote a correlation to a cause. Each is the phrasing a
  // well-meaning rewrite reaches for, and each would send an operator to change
  // a DNS record nothing implicated.
  for (const claim of [
    'caused by',
    'root cause',
    'is broken',
    'at fault',
    'to blame',
    'because of',
    'the culprit',
    'is failing',
    'responsible for',
  ]) {
    expect(everything.toLowerCase()).not.toContain(claim)
  }
})

// And it is said, not merely avoided: an operator has to be told that the row is
// a correlation, or they will supply the causal reading themselves.
test('the panel says outright that a shared value is not a reason', () => {
  expect(INCIDENTS_INTRO).toMatch(/concentrated among them rather than spread across the pool/i)
  expect(INCIDENTS_INTRO).toMatch(/does not say the shared value is why/i)
  // Two dimensions can carry one problem, which is why a row is a place to look
  // and not an answer.
  expect(INCIDENTS_INTRO).toMatch(/two dimensions can carry one underlying problem/i)
  // And the counts are named as the operator's own check on the inference.
  expect(INCIDENTS_INTRO).toMatch(/so you can disagree with the inference/i)
})

// Design §7, and deliberately not either of the sentences the identity panel and
// the route matrix carry: this one needs two reasons, and the second — that the
// destination axis is steerable inside a workspace — does not expire when
// calibration data arrives.
test('the panel says it gates nothing, and gives both reasons', () => {
  expect(INCIDENTS_GATES_NOTHING).toMatch(/no threshold, lane or promotion decision reads any of it/i)
  expect(INCIDENTS_GATES_NOTHING).toMatch(/guesses nobody has calibrated/i)
  expect(INCIDENTS_GATES_NOTHING).toMatch(/steerable by whoever controls a mailbox domain's MX/i)
})
