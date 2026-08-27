import { expect, test, type Locator, type Page, type Route } from '@playwright/test'

/**
 * The warmup pool surfaces in a real browser.
 *
 * The unit tests assert the copy rules against a mocked `fetch`. What they cannot
 * see is the integration this screen actually depends on: whether the lazy panel
 * fetches only when opened, whether a lower-bounded zero survives rendering as
 * "Not established" rather than a confident 0%, and — the one with history —
 * whether the lane badge reflects the lane the API returned or the SAFE DEFAULT it
 * falls back to. `/warmup/overview` once shipped without populating `lane` at all;
 * every mailbox rendered "Proving", nothing failed, and no test caught it because
 * the fallback is silent by design. A browser test is what closes that class.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as deliverability.spec.ts).
 */

const WITHHELD_MAILBOX = 'mailbox-withheld'

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

/**
 * Three participants, each carrying a shape that renders dishonestly if the copy
 * rules are dropped.
 *
 * The first two have two axes that DISAGREE, which is the whole point of the
 * split: reputation-healthy but withheld from the pool, and the reverse. A UI that
 * collapsed the axes would render these identically.
 *
 * They also cover the three tabbed-placement readings: a measured rate over its
 * own smaller denominator, an absence nothing could measure, and a genuine
 * measured zero. Every mailbox is served as provider `smtp` by the mailboxes route
 * below, deliberately: tab capability belongs to the reader that produced each
 * observation (design §5), never to the mailbox row, so the UI must take these
 * readings from the payload and not from `provider`.
 *
 * And they carry the three observed-identity shapes, which fail the same way:
 * fully stamped (with a genuine `fail` and a `none` that must not read alike),
 * never stamped (permanently `unknown`, which is our blind spot and not their
 * failure), and never observed at all.
 */
const OVERVIEW = {
  pool_size: 3,
  incidents_min_pool: 4,
  active: true,
  mailboxes: [
    {
      mailbox_id: WITHHELD_MAILBOX,
      email: 'withheld@acme.test',
      enabled: true,
      health_state: 'healthy',
      health_reason: '',
      lane: 'quarantine',
      lane_reason: 'quarantined: campaign hard-bounce rate above the pause threshold',
      today_sent: 0,
      today_target: 0,
      placement_sample_7d: 40,
      // 25 of those 40 observations came from a reader that could see a tab, so
      // the tabbed rate's denominator is its own number, not the 40 beside it.
      tabbed_rate_7d: 0.35,
      tab_capable_sample_7d: 25,
      inbox_rate_7d: 0.9,
      spam_rate_7d: 0.1,
      // Fully stamped: the receiver reported on all three checks, and two of the
      // answers are the ones most often rendered as each other. `spf_result:
      // fail` is a genuine negative and must read as one — while saying it gates
      // nothing, because this mailbox is withheld for its CAMPAIGN BOUNCE rate
      // (see lane_reason above) and an operator who "fixes" SPF to get it back
      // into the pool has been sent to the wrong problem entirely. `dmarc_result:
      // none` is the receiver saying it looked and found no DMARC record — a
      // finding, and not the same statement as the row below.
      identity: {
        dkim_domain: 'acme.test',
        return_path_domain: 'bounces.acme.test',
        spf_result: 'fail',
        dkim_result: 'pass',
        dmarc_result: 'none',
        observed_at: '2026-08-14T08:15:00Z',
      },
    },
    {
      mailbox_id: 'mailbox-proving',
      email: 'proving@acme.test',
      enabled: true,
      health_state: 'unknown',
      health_reason: 'not enough fresh placement evidence',
      lane: 'probation',
      lane_reason: 'awaiting a qualified clean window',
      today_sent: 2,
      today_target: 5,
      placement_sample_7d: 3,
      // Nothing that observed this mailbox could report a category. Not "no tabs".
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 0,
      inbox_rate_7d: null,
      spam_rate_7d: null,
      // Never stamped: this mailbox's partners run providers that write no
      // Authentication-Results anyone can be trusted for (RFC 8601 §5), so all
      // three verdicts are permanently `unknown`. That is an absence of
      // observation on OUR side, not a bad result on theirs — three red "fail"
      // chips here would report broken authentication that nobody ever reported,
      // for the whole class of providers that stamp nothing. The empty
      // dkim_domain is the same kind of gap: unsigned (or unparseable), which is
      // a fact to state, not a cell to leave blank.
      identity: {
        dkim_domain: '',
        return_path_domain: 'proving.acme.test',
        spf_result: 'unknown',
        dkim_result: 'unknown',
        dmarc_result: 'unknown',
        observed_at: '2026-08-14T07:00:00Z',
      },
    },
    {
      mailbox_id: 'mailbox-primary',
      email: 'primary@acme.test',
      enabled: true,
      health_state: 'healthy',
      health_reason: '',
      lane: 'healthy',
      lane_reason: '',
      today_sent: 6,
      today_target: 6,
      placement_sample_7d: 30,
      // Zero over a REAL sample: everything a reader could categorise landed in
      // the primary inbox. A measurement, and the opposite of the row above.
      tabbed_rate_7d: 0,
      tab_capable_sample_7d: 18,
      inbox_rate_7d: 0.97,
      spam_rate_7d: 0.03,
      // Nothing observed yet: no warmup message from this mailbox has been polled
      // with identity facts on it. Distinct from the row above, and rendering the
      // two alike is the defect — five "unknown" verdicts here would claim five
      // checks came back empty when in truth no message has been read at all.
      identity: null,
    },
  ],
}

/**
 * Destination-route matrices, keyed by mailbox id and served from the warmup
 * DETAIL endpoint each enrolled card already fetches for its sparkline — so the
 * routes disclosure costs a lazy chunk and no request of its own.
 *
 * Three shapes, each of which renders dishonestly the moment a copy rule is
 * dropped:
 *
 * `mailbox-withheld` is the headline case and the reason the split exists at all:
 * mail to Google is clean, and mail to Microsoft goes to spam more than half the
 * time. Pooled into one rate those two collapse into a single blended number that
 * understates the Microsoft problem and slanders the Google route, so the rows
 * have to show visibly different figures over visibly different denominators —
 * 55% of 60 and 1% of 400 are not comparable, and a matrix invites exactly that
 * comparison. It also carries the two destinations most likely to be flattened
 * into each other: `other` is RESOLVED and merely neither Google nor Microsoft (a
 * finding about the receiver), while `unknown` is our MX lookup not having
 * happened (a gap on our side). Rendering `unknown` as a fourth provider beside
 * the other three invents a place the mail was delivered to.
 *
 * `mailbox-primary` is a single-ESP pool, which design §3 calls the worst
 * misreading this feature enables. Warmup partners are the workspace's OWN
 * connected mailboxes, so an all-Google pool can only ever be measured against
 * Google — and this one is spotless: 100% inbox and a genuine measured 0% spam. A
 * tidy one-row matrix would tell its operator that their Microsoft delivery is
 * healthy when no warmup mail was ever sent to Microsoft.
 *
 * `mailbox-proving` is §8's degradation: the MX sweep is behind, so the single row
 * records no destination at all and every rate is under the sample floor. Naming a
 * provider here would invent one, and a null rate rendered as a clean 0% would
 * report a result from evidence nobody has.
 *
 * Every tab-capable count is a subset of its own row's placement count, because
 * that is the only shape the server can produce: a route with 4 observations
 * cannot have 60 tab-capable ones, and a fixture that says otherwise tests a
 * rendering that will never happen.
 */
const ROUTES: Record<string, unknown[]> = {
  'mailbox-withheld': [
    {
      destination_esp: 'google',
      placement_sample_7d: 400,
      inbox_rate_7d: 0.99,
      spam_rate_7d: 0.01,
      tabbed_rate_7d: 0.12,
      tab_capable_sample_7d: 300,
    },
    {
      destination_esp: 'microsoft',
      placement_sample_7d: 60,
      inbox_rate_7d: 0.45,
      spam_rate_7d: 0.55,
      // Nothing that received this route's mail could report a category, which is
      // an absence — not a clean primary inbox on the one route that is failing.
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 0,
    },
    {
      destination_esp: 'other',
      placement_sample_7d: 24,
      // Zero over real samples, on BOTH kinds of denominator: no mail on this
      // route went to spam, and everything a reader could categorise reached the
      // primary inbox. Measurements, and the only good news the matrix can
      // deliver — a "falsy means unknown" implementation throws all three away.
      // The placement zero is deliberate: the tabbed rate has its own null
      // handling, so a matrix whose only zero is a tabbed one leaves the
      // placement path untested.
      inbox_rate_7d: 1,
      spam_rate_7d: 0,
      tabbed_rate_7d: 0,
      tab_capable_sample_7d: 6,
    },
    {
      destination_esp: 'unknown',
      placement_sample_7d: 9,
      inbox_rate_7d: null,
      spam_rate_7d: null,
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 0,
    },
  ],
  'mailbox-primary': [
    {
      destination_esp: 'google',
      placement_sample_7d: 120,
      inbox_rate_7d: 1,
      spam_rate_7d: 0,
      tabbed_rate_7d: 0.05,
      tab_capable_sample_7d: 40,
    },
  ],
  'mailbox-proving': [
    {
      destination_esp: 'unknown',
      placement_sample_7d: 7,
      inbox_rate_7d: null,
      spam_rate_7d: null,
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 0,
    },
  ],
}

/* ------------------------------------------------- correlated degradation */

/**
 * One participant, reduced to what a correlation reads: whether it is degraded,
 * and on which axis.
 *
 * Both axes are represented in the pool below on purpose. Health and lane are
 * independent by design and a shared cause surfaces on either — a filtering relay
 * lands on health, an authentication fault lands on the lane — so a fixture that
 * degraded only `health_state` would pass against a UI that never looked at the
 * lane, and the withheld half of every real incident would go uncounted.
 */
function poolMember(id: string, email: string, degradation: 'health' | 'lane' | 'none') {
  return {
    mailbox_id: id,
    email,
    enabled: true,
    health_state: degradation === 'health' ? 'paused' : 'healthy',
    health_reason: degradation === 'health' ? 'spam rate above the pause threshold' : '',
    lane: degradation === 'lane' ? 'quarantine' : 'healthy',
    lane_reason: degradation === 'lane' ? 'quarantined: hard-bounce rate above the pause threshold' : '',
    today_sent: 0,
    today_target: 4,
    placement_sample_7d: 40,
    tabbed_rate_7d: null,
    tab_capable_sample_7d: 0,
    inbox_rate_7d: 0.9,
    spam_rate_7d: 0.1,
    identity: null,
  }
}

/**
 * Eleven participants, five of them degrading: `mb-1`..`mb-4` on the two shared
 * values below, and `mb-5` degrading on its own with nothing in common with them.
 *
 * `mb-5` is the load-bearing member. Without a degraded mailbox OUTSIDE both
 * cohorts every correlation divides by a clean pool, every lift is enormous, and
 * a rendering that dropped the outside count entirely would look correct — the
 * one number an operator needs to disagree with the inference is the one a
 * fixture without `mb-5` cannot miss.
 */
const CORRELATED_POOL = [
  poolMember('mb-1', 'one@acme.test', 'health'),
  poolMember('mb-2', 'two@acme.test', 'health'),
  poolMember('mb-3', 'three@acme.test', 'lane'),
  poolMember('mb-4', 'four@acme.test', 'lane'),
  poolMember('mb-5', 'five@acme.test', 'health'),
  poolMember('mb-6', 'six@acme.test', 'none'),
  poolMember('mb-7', 'seven@partner.test', 'none'),
  poolMember('mb-8', 'eight@partner.test', 'none'),
  poolMember('mb-9', 'nine@partner.test', 'none'),
  poolMember('mb-10', 'ten@partner.test', 'none'),
  poolMember('mb-11', 'eleven@partner.test', 'none'),
]

/**
 * The same four degraded mailboxes, correlated twice — which is the shape §8's
 * copy rule is really about, and the one that talks an operator into a cause.
 *
 * The signing row is concentrated 4.8×: four of the five mailboxes signing as
 * `mail.acme.test` are degrading, against one of the other six. The destination
 * row is the same four mailboxes at 2.3×, because they also happen to send to
 * Microsoft along with three healthy ones. Both are true. Neither says the DKIM
 * key or the relay is why, and a UI that badges them alike — or that shows only
 * the strongest — tells an operator to go and change a DNS record that nothing
 * here implicates.
 *
 * So the fixture pins the two readings a badge would erase: 4.8× and 2.3× have to
 * reach the screen as different numbers, and the marginal one has to say it may
 * be chance while the strong one does not.
 *
 * Every count is consistent with the pool above, because that is the only shape
 * the server can produce: a cohort of five whose outside is six, in a pool of
 * eleven, with the one degraded mailbox outside both cohorts counted in both
 * outside figures. A fixture whose arithmetic does not add up tests a rendering
 * that will never happen.
 */
const CORRELATED_INCIDENTS = [
  {
    dimension: 'signing_domain',
    value: 'mail.acme.test',
    member_mailbox_ids: ['mb-1', 'mb-2', 'mb-3', 'mb-4'],
    cohort_size: 5,
    degraded_inside: 4,
    cohort_outside: 6,
    degraded_outside: 1,
    lift: 4.8,
  },
  {
    // A contract token, not a provider's name. The route matrix already refuses
    // to put `microsoft` on a screen; the same provider must not arrive
    // lower-cased here just because it came through a different field.
    dimension: 'destination_route',
    value: 'microsoft',
    member_mailbox_ids: ['mb-1', 'mb-2', 'mb-3', 'mb-4'],
    cohort_size: 7,
    degraded_inside: 4,
    cohort_outside: 4,
    degraded_outside: 1,
    lift: 2.2857,
  },
]

/**
 * Two published observer-trust verdicts, and nothing acts on either of them
 * (security.md invariant 59) — this panel is the whole feature, so the browser is
 * where its two dishonest readings have to be ruled out.
 *
 * `mb-7` is deliberately one of the pool's HEALTHY mailboxes: the verdict is about
 * what it REPORTED as a recipient, not about how it sends, and a rendering that
 * treats it as a mailbox in trouble contradicts the card sitting below it. Its
 * arithmetic adds up — 59 of 130 is 45%, its Microsoft peers sit at 12%, and
 * 45/12 is the 3.8× the multiple rounds to.
 *
 * `mb-gone` is not in the pool at all, which is a shape the server really produces:
 * the window spans seven days, so a mailbox removed from warmup yesterday still has
 * reports in it. It must be named by its id rather than dropped — a verdict rendered
 * about nobody is worse than an ugly one. Its cohort is spotless (0% peers), the
 * case where an exact ratio would be a division by zero.
 *
 * Ordered worst-multiple first, as the contract says they arrive.
 */
const DISCOUNTED_OBSERVERS = [
  {
    observer_mailbox_id: 'mb-gone',
    cohort: 'other',
    spam: 30,
    total: 40,
    spam_rate: 0.75,
    cohort_spam_rate: 0,
    lift: 60,
  },
  {
    observer_mailbox_id: 'mb-7',
    cohort: 'microsoft',
    spam: 59,
    total: 130,
    spam_rate: 0.45,
    cohort_spam_rate: 0.12,
    lift: 3.75,
  },
]

const CORRELATED_OVERVIEW = {
  pool_size: CORRELATED_POOL.length,
  // The floor the API serves (warmup.MinIncidentPool). Without it the panel reads
  // as "no search was reported" and draws nothing — the honest response to a payload
  // that never said what the server was able to look across.
  incidents_min_pool: 4,
  active: true,
  mailboxes: CORRELATED_POOL,
  discounted_observers: DISCOUNTED_OBSERVERS,
  incidents: CORRELATED_INCIDENTS,
}

/**
 * The same degrading pool with nothing shared running through it — the fixture
 * that separates the two answers `incidents: []` can mean.
 *
 * The array is byte-identical to the one a perfectly healthy workspace gets, so
 * only the pool it arrives with says which sentence is true. Rendered as a tidy
 * "no incidents" this reads as reassurance on a pool where five mailboxes are
 * degrading; rendered as an apology it reads as a failure to compute something.
 * It is neither: five mailboxes are degrading and nothing shared runs through
 * them, which is information an operator acts on by working through them one at
 * a time.
 */
const UNATTRIBUTED_OVERVIEW = {
  pool_size: CORRELATED_POOL.length,
  // The floor the API serves (warmup.MinIncidentPool). Without it the panel reads
  // as "no search was reported" and draws nothing — the honest response to a payload
  // that never said what the server was able to look across.
  incidents_min_pool: 4,
  active: true,
  mailboxes: CORRELATED_POOL,
  incidents: [],
}

/* ------------------------------------------------------------- sentinels */

/**
 * A pool with one designated sentinel, and the two evidence labels side by side.
 *
 * `reference@acme.test` is the sentinel: healthy, in the healthy lane, and exposed
 * to every lane on purpose — the three facts a UI that treated "sentinel" as a
 * lane could not show at once.
 *
 * `watched@acme.test` is degrading and CORROBORATED, which is the pairing the
 * feature exists for: the mailbox whose own lane-mates cannot be trusted to
 * measure it is the one a sentinel is there to measure.
 *
 * `plain@acme.test` is peer-only, and it is the reading most at risk in a real
 * browser: rendered with any warning tone it becomes a defect to chase on every
 * card of every pool that has no sentinel — which is most of them.
 *
 * The pool is deliberately INSIDE the advised share (1 of 3), so the advisory's
 * absence is a fact this fixture asserts rather than a coincidence.
 */
const SENTINEL_POOL = [
  {
    ...poolMember('mb-reference', 'reference@acme.test', 'none'),
    is_sentinel: true,
    evidence_confidence: 'sentinel_corroborated',
    sentinel_observations_7d: 6,
  },
  {
    ...poolMember('mb-watched', 'watched@acme.test', 'health'),
    is_sentinel: false,
    evidence_confidence: 'sentinel_corroborated',
    sentinel_observations_7d: 9,
  },
  {
    ...poolMember('mb-plain', 'plain@acme.test', 'none'),
    is_sentinel: false,
    evidence_confidence: 'peer_only',
    sentinel_observations_7d: 0,
  },
]

const SENTINEL_OVERVIEW = {
  pool_size: SENTINEL_POOL.length,
  incidents_min_pool: 4,
  active: true,
  sentinel_count: 1,
  // The server's own verdict, never recomputed client-side: 1 of 3 is inside the
  // advised share, so nothing is advised.
  sentinel_pool_oversized: false,
  sentinel_pool_share: 0.5,
  mailboxes: SENTINEL_POOL,
  incidents: [],
}

/** Every body sent to the sentinel endpoint, in order. */
let sentinelWrites: string[] = []

/**
 * The detail payload behind one mailbox's card. `series` is deliberately empty:
 * the sparkline is not what these fixtures are about, and its "not enough history
 * yet" line keeps the card's own text clear of the matrix under test.
 */
function warmupDetail(mailboxId: string) {
  const entry = OVERVIEW.mailboxes.find((m) => m.mailbox_id === mailboxId)
  return {
    participant: {
      mailbox_id: mailboxId,
      enabled: true,
      start_volume: 4,
      max_volume: 40,
      ramp_increment: 2,
      reply_rate: 0.3,
      health_state: entry?.health_state ?? 'unknown',
      health_reason: entry?.health_reason ?? '',
      lane: entry?.lane ?? 'probation',
      started_at: '2026-08-01T00:00:00Z',
      today_sent: entry?.today_sent ?? 0,
      today_target: entry?.today_target ?? 0,
    },
    series: [],
    routes: ROUTES[mailboxId] ?? [],
  }
}

/**
 * One transition carrying the two shapes most likely to be rendered dishonestly: a
 * spam rate of 0 over a REAL sample (a floor below the policy minimum, not a
 * measurement), and no recorded lane (an entry predating pool lanes).
 */
const TRANSITIONS = {
  transitions: [
    {
      id: 'transition-1',
      created_at: '2026-08-13T09:30:00Z',
      from_state: 'healthy',
      to_state: 'watch',
      reason_code: 'campaign_bounce_watch',
      reason: 'campaign hard-bounce rate above the watch threshold',
      from_lane: null,
      to_lane: null,
      lane_reason_code: null,
      lane_reason: null,
      placement_samples: 5,
      spam_rate: 0,
      bounce_population: 'campaign',
      bounce_samples: 200,
      bounce_rate: 0.061,
      complaint_samples: 0,
      complaint_rate: 0,
      invalid_tokens: 0,
      policy_version: 'warmup-phase1-v1',
    },
  ],
}

/** Counts how many times the transitions endpoint was actually hit. */
let transitionRequests = 0

/**
 * The overview this test is running against. Defaults to the three-mailbox pool
 * above, which reports no `incidents` field at all — the shape a server predating
 * correlation sends, and the one the rest of this file exercises.
 */
let overview: { mailboxes: { mailbox_id: string; email: string }[] } = OVERVIEW

/** Serve a different pool and reload onto it. */
async function serveOverview(page: Page, fixture: typeof overview) {
  overview = fixture
  await page.reload()
}

async function mockApi(page: Page) {
  transitionRequests = 0
  sentinelWrites = []
  overview = OVERVIEW
  await page.route('**/api/v1/**', async (route: Route) => {
    const path = new URL(route.request().url()).pathname

    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({
        access_token: 'browser-test-token',
        expires_in: 900,
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
      }))
    }
    if (path.endsWith('/auth/me')) {
      return route.fulfill(json({
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
        email_verified: true,
      }))
    }

    if (path.includes('/transitions')) {
      transitionRequests += 1
      return route.fulfill(json(TRANSITIONS))
    }
    // Designation. Recorded rather than merely answered: the property under test
    // is that NOTHING is written until the operator has seen what it costs, and
    // only the request log can show the difference between "asked" and "did".
    if (path.endsWith('/sentinel')) {
      sentinelWrites.push(route.request().postData() ?? '')
      return route.fulfill(json({ ...warmupDetail('mb-plain').participant, is_sentinel: true }))
    }
    if (path.endsWith('/warmup/overview')) return route.fulfill(json(overview))

    // One mailbox's warmup detail — the series behind its sparkline and the
    // destination-route matrix behind its Routes disclosure. Matched before the
    // bare `/mailboxes` list below, which this path does not end with.
    const detail = /\/mailboxes\/([^/]+)\/warmup$/.exec(path)
    if (detail) return route.fulfill(json(warmupDetail(detail[1] ?? '')))

    if (path.endsWith('/pulse')) {
      return route.fulfill(json({
        mailboxes: { total: 2, active: 2, paused: 0, error: 0 },
        warmup: { pool: 2, unknown: 1, healthy: 0, watch: 0, at_risk: 0, probation: 1, quarantine: 1 },
        campaigns: { total: 0, running: 0, draft: 0, paused: 0 },
        contacts: { total: 0 },
        sending: { sent_today: 0, daily_cap: 100 },
        inbox: { unread: 0, interested: 0 },
        attention: [],
      }))
    }
    // The cards come from the MAILBOX list, joined to the overview entries by id —
    // the overview alone only feeds the header counts, which is why an empty list
    // here renders "No mailboxes to warm" over a pool of two.
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json(overview.mailboxes.map((m) => ({
        id: m.mailbox_id,
        email: m.email,
        provider: 'smtp',
        status: 'active',
        warmup_enabled: true,
      }))))
    }
    if (path.endsWith('/campaigns')) return route.fulfill(json([]))

    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
}

/**
 * Addressed through the accessibility tree rather than test-only markup: the card
 * is a list item and the disclosure already carries an aria-label naming its
 * mailbox, so the test exercises the same handles a screen reader would.
 */
function card(page: Page, email: string) {
  return page.getByRole('listitem').filter({ hasText: email }).first()
}

async function openHistory(page: Page, email: string) {
  await page.getByRole('button', { name: `Change history for ${email}` }).click()
}

/** The disclosure's own region, resolved via the button's aria-controls. */
function historyPanel(page: Page) {
  return page.getByText('Change history', { exact: true }).locator('..')
}

/**
 * Opens the observed-identity disclosure and returns the region it controls,
 * resolved through `aria-controls` — the same hop a screen reader makes, so the
 * scoping is the accessibility wiring rather than a selector maintained beside
 * it. Scoping matters here: the metrics row on the same card already carries a
 * "gates nothing" note, so a card-wide assertion would pass on the wrong text.
 */
async function openIdentity(page: Page, email: string) {
  const toggle = page.getByRole('button', { name: `Sending identity for ${email}` })
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')

  const region = await toggle.getAttribute('aria-controls')
  expect(region, 'the identity disclosure must name the region it controls').toBeTruthy()
  return page.locator(`[id="${region}"]`)
}

/**
 * Opens the destination-route disclosure and returns the matrix it controls,
 * resolved through `aria-controls` — the same hop a screen reader makes. Scoping
 * to the panel is not cosmetic: the metrics row on the same card and the identity
 * panel beside it BOTH carry a "gates nothing" note, so a card-wide assertion
 * about this one would pass on somebody else's sentence.
 */
async function openRoutes(page: Page, email: string) {
  const toggle = page.getByRole('button', { name: `Destination routes for ${email}` })
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')

  const region = await toggle.getAttribute('aria-controls')
  expect(region, 'the routes disclosure must name the region it controls').toBeTruthy()
  const panel = page.locator(`[id="${region}"] [data-slot="warmup-routes"]`)
  await expect(panel).toBeVisible()
  return panel
}

/**
 * One destination's row, found through the name the MATRIX gives it rather than
 * the contract token — so a row that quietly renders `unknown` as a provider
 * cannot be found under the name it should have had.
 *
 * Matched on the destination node alone. The unresolved row's explanation names
 * "Another provider" on purpose, to disown it, so a whole-row text filter would
 * return two rows for one destination and the collapse this guards against would
 * pass unnoticed. The inner locator is built from `page` because Playwright
 * re-anchors it to the row it is filtering.
 */
function routeRow(page: Page, panel: Locator, destination: string | RegExp): Locator {
  return panel
    .locator('tbody tr')
    .filter({ has: page.locator('[data-slot="route-destination"]', { hasText: destination }) })
}

/** The row's rendered destination name, without the sentence explaining it. */
function destinationOf(row: Locator): Locator {
  return row.locator('[data-slot="route-destination"]')
}

/**
 * One row's rate VALUES in column order, without their populations or their
 * explanatory sentences. Scoped for the same reason the destination is: a spam
 * rate that silently became "Not established" still reads plausibly when the
 * surrounding sample text is swept into the comparison.
 */
function ratesOf(row: Locator): Locator {
  return row.locator('[data-slot="route-rate"]')
}

function populationsOf(row: Locator): Locator {
  return row.locator('[data-slot="route-population"]')
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
  await signIn(page)
  await page.goto('/app/warmup')
})

// The regression that motivated this file: the lane came back absent, every card
// fell back to "probation", and the screen looked entirely plausible.
test('the lane badge shows the lane the API returned, not the safe default', async ({ page }) => {
  const withheld = card(page, 'withheld@acme.test')
  await expect(withheld).toContainText(/withheld|quarantine/i)
  await expect(withheld).not.toContainText(/proving/i)

  // And the two axes stay distinguishable: this mailbox is reputation-healthy AND
  // withheld, which a UI that collapsed them could not show.
  await expect(withheld).toContainText(/healthy/i)
})

test('the history panel does not fetch until it is opened', async ({ page }) => {
  await expect(card(page, 'withheld@acme.test')).toBeVisible()
  expect(transitionRequests).toBe(0)

  await openHistory(page, 'withheld@acme.test')

  await expect.poll(() => transitionRequests).toBe(1)
})

// A rate of 0 over a real sample is a floor, not a measurement. Rendering it as a
// clean 0% would tell an operator the mailbox is spotless on the strength of
// evidence the policy already refused to act on.
test('a lower-bounded zero renders as unestablished, never as a clean percentage', async ({ page }) => {
  await openHistory(page, 'withheld@acme.test')

  const panel = historyPanel(page)
  await expect(panel).toBeVisible()
  await expect(panel).toContainText(/not established/i)
  await expect(panel).not.toContainText(/\b0(\.0+)?\s*%/)
})

// Tabs do not exist as a concept over IMAP, so a null tabbed rate means nothing
// observing this mailbox could report a category. A 0% here would tell an operator
// their mail has perfect primary placement on the strength of a measurement that
// never happened — the same silent-fallback class as the missing `lane` above.
test('a mailbox nothing could categorise says so, and shows no tabbed percentage', async ({ page }) => {
  const proving = card(page, 'proving@acme.test')

  await expect(proving).toContainText('Not detectable — no partner could report a tab')
  // Scoped to the tabbed metric: the row legitimately shows inbox and spam
  // percentages, so only "a figure directly after the tabbed label" is the defect.
  await expect(proving).not.toContainText(/tabbed 7d\s*[<\d]/)
  // And it is not read as a penalty: nothing gates on this number.
  await expect(proving).toContainText(/tabbed 7d[^·]*· gates nothing/)
})

// The tabbed denominator is smaller than the placement one by construction, so the
// rate has to arrive with its own sample count or an operator compares two
// different populations.
test('a measured tabbed rate carries its own denominator, not the observations count', async ({ page }) => {
  const withheld = card(page, 'withheld@acme.test')

  await expect(withheld).toContainText('tabbed 7d 35% of 25 tab-capable · gates nothing')
  await expect(withheld).toContainText('40 observations')
  await expect(withheld).not.toContainText(/not detectable/i)
})

// The inverse of the null case, and the one a "falsy means unknown" implementation
// gets wrong: zero over a real sample is a measurement — everything a reader could
// categorise reached the primary inbox — and must render as the 0% it is.
test('a measured zero renders as 0%, not as an absence', async ({ page }) => {
  const primary = card(page, 'primary@acme.test')

  await expect(primary).toContainText('tabbed 7d 0% of 18 tab-capable · gates nothing')
  await expect(primary).not.toContainText(/not detectable/i)
})

// The two verdicts a browser is most likely to flatten into one another. `none`
// is the receiver reporting that it looked and there was no DMARC record;
// `unknown` is nobody having reported anything. Asserted across two cards in one
// test, because the defect is not what either says on its own — it is the two
// saying the same thing.
test('a checked-and-absent verdict and an unreported one do not read alike', async ({ page }) => {
  const stamped = await openIdentity(page, 'withheld@acme.test')
  await expect(stamped).toContainText(/no DMARC record/i)
  await expect(stamped).toContainText(/looked and found/i)
  // All three of this mailbox's verdicts were reported, so the unreported
  // wording must appear nowhere on it. This is the assertion that catches a
  // collapse: the explanatory sentence alone still reads plausibly when `none`
  // is quietly given `unknown`'s words, and only the verdict itself gives it away.
  await expect(stamped).not.toContainText(/not reported by the receiver/i)

  const neverStamped = await openIdentity(page, 'proving@acme.test')
  await expect(neverStamped).toContainText(/not reported by the receiver/i)
  await expect(neverStamped).not.toContainText(/looked and found/i)
  await expect(neverStamped).not.toContainText(/no DMARC record/i)
})

// A failing verdict is a real negative and reads as one — and it decides
// nothing: this mailbox is withheld over its campaign bounce rate, so an
// operator who reads the SPF failure as the reason has been sent to fix the
// wrong thing. Same marker the tabbed rate carries, for the same reason.
test('a failing verdict is visible and says plainly that it gates nothing', async ({ page }) => {
  const identity = await openIdentity(page, 'withheld@acme.test')

  await expect(identity).toContainText(/fail[^·]*· gates nothing/)
  await expect(identity).toContainText(/no threshold, lane or promotion decision reads any of it/i)
  // The identity is one observation, and a dated one — not a live configuration.
  await expect(identity).toContainText(/observed/i)
})

// A provider that stamps no Authentication-Results leaves a mailbox permanently
// unknown. Presenting that as failure would tell an operator their
// authentication is broken on the strength of a check nobody ran.
test('a never-stamped mailbox reads as unobserved, never as failing', async ({ page }) => {
  const identity = await openIdentity(page, 'proving@acme.test')

  await expect(identity).toContainText(/not reported by the receiver/i)
  await expect(identity).toContainText(/absence of observation, not a failed check/i)
  // And the silence is named as normal and permanent, so three unreported rows
  // do not read as three things to go and fix.
  await expect(identity).toContainText(/stay unreported however well the mail authenticates/i)
  // Nothing failed, so nothing carries the failure disclaimer either.
  await expect(identity).not.toContainText(/· gates nothing/)
  // And the limitation is the partner's, not this mailbox's own provider.
  await expect(identity).not.toContainText(/this provider|your provider/i)
})

// An unsigned message has no d= domain. Rendering the gap as a gap makes it look
// like the panel failed to load a value that exists.
test('an unsigned message says it was not signed', async ({ page }) => {
  const identity = await openIdentity(page, 'proving@acme.test')

  await expect(identity).toContainText('DKIM signing domain')
  await expect(identity).toContainText(/not signed/i)
})

// No observation has carried identity facts yet, which is not the same as five
// checks coming back empty — and is the reading a "default everything to
// unknown" implementation would silently produce.
test('a mailbox with no observed identity says so, and reports no verdicts', async ({ page }) => {
  const identity = await openIdentity(page, 'primary@acme.test')

  await expect(identity).toContainText(/has been observed with identity facts yet/i)
  await expect(identity).toContainText(/not a failed check/i)
  await expect(identity).not.toContainText(/not reported by the receiver/i)
  await expect(identity).not.toContainText('DKIM signing domain')
})

test('an entry predating pool lanes says so instead of inventing a lane', async ({ page }) => {
  await openHistory(page, 'withheld@acme.test')

  const panel = historyPanel(page)
  await expect(panel).toContainText(/predates pool lanes|no pool lane/i)
  // The bounce figure is attributed, so the operator can tell whose mail it counted.
  await expect(panel).toContainText(/campaign/i)
})

/* ------------------------------------------------- destination-route matrix */

// The matrix carries the largest copy table on this screen and answers a question
// nobody asks until the pooled rates above have already looked wrong, so it ships
// as a lazy chunk behind a disclosure. A page of ten collapsed cards must not
// mount ten of them.
test('the destination matrix is not mounted until the operator opens it', async ({ page }) => {
  const withheld = card(page, 'withheld@acme.test')
  await expect(withheld).toBeVisible()

  // The REGION the toggle controls, asserted to have no CHILD ELEMENTS — not
  // merely "no matrix anywhere on the card", and not `toBeEmpty()`.
  //
  // A lazily-mounted panel occupies its region as a Suspense fallback from its
  // first commit, so "the matrix has not appeared yet" is equally true of a panel
  // that mounts eagerly and is still fetching its chunk. And `toBeEmpty()` is no
  // better: it asserts the node has no TEXT, while the fallback is a skeleton —
  // child elements, not a word between them. Verified against the revert: with
  // the panel mounted eagerly, `toBeEmpty()` passed and this counts 1.
  const toggle = page.getByRole('button', { name: 'Destination routes for withheld@acme.test' })
  const region = await toggle.getAttribute('aria-controls')
  expect(region, 'the routes disclosure must name the region it controls').toBeTruthy()
  await expect(page.locator(`[id="${region}"] > *`)).toHaveCount(0)

  const panel = await openRoutes(page, 'withheld@acme.test')

  await expect(panel.getByRole('table')).toBeVisible()
})

// THE case the split exists for. Pooled, these two destinations report one
// blended number that understates the Microsoft problem and slanders the Google
// route; split, they are plainly two different findings — and each is legible
// only against its own denominator, since 55% of 60 and 1% of 400 are not the
// same population.
test('a clean route and a failing one read as two findings, each over its own sample', async ({ page }) => {
  const panel = await openRoutes(page, 'withheld@acme.test')
  const google = routeRow(page, panel, 'Google')
  const microsoft = routeRow(page, panel, 'Microsoft')

  await expect(ratesOf(google)).toHaveText(['99%', '1%', '12%'])
  await expect(ratesOf(microsoft)).toHaveText(['45%', '55%', 'Not detectable'])

  await expect(populationsOf(google).first()).toHaveText('of 400 observations on this route')
  await expect(populationsOf(microsoft).first()).toHaveText('of 60 observations on this route')
  // The failing route's tabbed cell is an absence, not a clean primary inbox —
  // the reading that would otherwise soften the row that matters most.
  await expect(microsoft).toContainText(/not a clean primary-inbox result/i)

  // Four destinations is a matrix and stands on its own; warning about a
  // single-route pool that isn't one trains an operator to skip the note in the
  // one case where it matters.
  await expect(panel.locator('[data-slot="route-sole-destination"]')).toHaveCount(0)
})

// `unknown` is not a fourth provider — it is the recipient domain's MX having not
// been resolved, so nobody recorded where the mail went. `other` is its opposite:
// resolved, and merely neither Google nor Microsoft. Rendered alike, they tell an
// operator we know where mail was delivered when we do not.
test('an unresolved destination is not a fourth provider, and is not "another provider"', async ({ page }) => {
  const panel = await openRoutes(page, 'withheld@acme.test')
  const other = routeRow(page, panel, 'Another provider')
  const unresolved = routeRow(page, panel, /not resolved/i)

  await expect(destinationOf(unresolved)).toHaveText('Destination not resolved')
  await expect(destinationOf(unresolved)).not.toHaveText(/provider|google|microsoft/i)
  await expect(destinationOf(other)).toHaveText('Another provider')

  // The sentences say which of the two facts each row is, and neither borrows the
  // other's. Asserted on the rows, not the names, because that is where a
  // collapse would hide.
  await expect(other).toContainText(/resolved, and neither Google nor Microsoft/i)
  await expect(other).not.toContainText(/has not been resolved yet/i)
  await expect(unresolved).toContainText(/has not been resolved yet/i)

  // And the raw contract token never reaches the screen at all.
  await expect(panel).not.toContainText(/\bunknown\b/i)
})

// The pair a "falsy means unknown" implementation gets wrong, asserted inside ONE
// matrix so neither reading can be explained away by the fixture: 0% spam over 24
// observations is the only good news this feature can deliver, and a null over 9
// observations is a rate nobody has yet — printed as 0% it would be a clean result
// invented out of thin evidence.
//
// The measured zero is asserted on the whole row, placement columns included. A
// tabbed zero alone would leave the placement path untested, because the two rates
// answer a null through different functions — and the revert that collapses only
// the placement one then sails past.
test('a measured zero and an unestablished rate are different readings in one matrix', async ({ page }) => {
  const panel = await openRoutes(page, 'withheld@acme.test')
  const other = routeRow(page, panel, 'Another provider')
  const unresolved = routeRow(page, panel, /not resolved/i)

  await expect(ratesOf(other)).toHaveText(['100%', '0%', '0%'])
  await expect(ratesOf(unresolved)).toHaveText(['Not established', 'Not established', 'Not detectable'])
  await expect(unresolved).toContainText(/not a zero/i)
  // Over its own nine observations, not the 400 on the Google row above it.
  await expect(populationsOf(unresolved).first()).toHaveText('over 9 observations on this route')
})

// Design §3, and the guard most likely to be quietly broken later. Warmup partners
// are the workspace's OWN mailboxes, so an all-Google pool has been measured
// against Google and nothing else. This row is spotless — 100% inbox, a genuine
// measured 0% spam — which is exactly why a bare one-row matrix would tell its
// operator that their Microsoft delivery is healthy when no warmup mail was ever
// sent to Microsoft.
test('a single-destination pool is warned about, above the clean row it qualifies', async ({ page }) => {
  const panel = await openRoutes(page, 'primary@acme.test')
  const note = panel.locator('[data-slot="route-sole-destination"]')

  await expect(note).toContainText(/only one destination observed: Google/i)
  await expect(note).toContainText(/says nothing about how it is delivered to any other provider/i)
  await expect(note).toContainText(/one clean row is not a clean matrix/i)
  // It names why, so the limitation is understood rather than merely warned about.
  await expect(note).toContainText(/your own connected mailboxes/i)
  await expect(ratesOf(routeRow(page, panel, 'Google'))).toHaveText(['100%', '0%', '5%'])

  // Physically above the matrix, not merely earlier in the DOM: a footnote reaches
  // an operator after they have already drawn the wrong conclusion from a green
  // row, and only a laid-out browser can tell the two apart.
  const noteBox = await note.boundingBox()
  const tableBox = await panel.getByRole('table').boundingBox()
  if (!noteBox || !tableBox) throw new Error('the note and the matrix must both be laid out')
  expect(noteBox.y + noteBox.height).toBeLessThanOrEqual(tableBox.y)
})

// §8's degradation, in the browser: the MX sweep is behind, so the whole matrix is
// one row that records no destination. Naming a provider here would invent one,
// and the sole-destination note has to say that nothing is known about ANY of
// them — not that one was observed.
test('a matrix that is one unresolved row says nothing is known about any provider', async ({ page }) => {
  const panel = await openRoutes(page, 'proving@acme.test')
  const note = panel.locator('[data-slot="route-sole-destination"]')
  const unresolved = routeRow(page, panel, /not resolved/i)

  await expect(note).toContainText(/only one destination observed, and it is not resolved/i)
  await expect(note).toContainText(/nothing about delivery to Google, to Microsoft, or to anywhere else/i)
  await expect(destinationOf(unresolved)).toHaveText('Destination not resolved')
  await expect(ratesOf(unresolved)).toHaveText(['Not established', 'Not established', 'Not detectable'])
  // One row, and it is the only one — no invented Google or Microsoft beside it.
  await expect(panel.locator('tbody tr')).toHaveCount(1)
})

// Design §7, and deliberately NOT the sentence the tabbed rate and the identity
// panel carry. Those gate nothing because their signals are structurally
// unobservable on a whole provider class. A route rate is observable everywhere
// the route exists; what is missing is calibration — and that condition is meant
// to expire, where "cannot be observed" would outlive it.
test('the matrix says it gates nothing, and gives the calibration reason', async ({ page }) => {
  const panel = await openRoutes(page, 'withheld@acme.test')

  await expect(panel).toContainText(/no threshold, lane or promotion decision reads any of it/i)
  await expect(panel).toContainText(/nobody has yet seen what a normal per-route rate looks like/i)
  await expect(panel).toContainText(/it can, on every provider/i)
})

/* ------------------------------------------------- correlated degradation */

/**
 * The pool's correlation panel, addressed as the named region it is — the same
 * handle a screen reader uses, rather than a class or a test id maintained beside
 * it. Scoping to it is not cosmetic: the page's own stat strip counts at-risk
 * mailboxes and every card carries a lane reason, so a page-wide assertion about
 * degradation would pass on somebody else's text.
 */
function incidentsPanel(page: Page) {
  return page.getByRole('region', { name: 'Correlated degradation' })
}

/**
 * One correlation's row, found through the VALUE the panel gives it. Matched on
 * the value node alone, because every row also carries a dimension label, three
 * figures and a sentence — a row whose value silently became something else still
 * reads plausibly when all of that is swept into the filter.
 */
function incidentRow(page: Page, panel: Locator, value: string) {
  return panel.locator('li').filter({ has: page.locator('[data-slot="incident-value"]', { hasText: value }) })
}

/** One row's three figures, in order, without their labels or their sentences. */
function figuresOf(row: Locator): Locator {
  return row.locator('[data-slot="incident-stat"]')
}

// THE case §8 is about, and the one a badge destroys. The same four mailboxes are
// correlated twice: strongly on their signing domain, marginally on their
// destination. Both readings are true, they are not the same finding, and an
// operator who cannot see the difference is being told to go and change a DNS
// record that nothing here implicates.
test('two correlations over the same mailboxes read as two strengths, not one badge', async ({ page }) => {
  await serveOverview(page, CORRELATED_OVERVIEW)
  const panel = incidentsPanel(page)
  await expect(panel).toBeVisible()

  await expect(panel.locator('[data-slot="incident-dimension"]')).toHaveText([
    'signing domain (DKIM)',
    'destination',
  ])
  // The provider is named the way the route matrix names it. `microsoft` is our
  // contract's word, and it must not reach a screen through a different field
  // than the one that already forbids it.
  await expect(panel.locator('[data-slot="incident-value"]')).toHaveText(['mail.acme.test', 'Microsoft'])

  const signing = incidentRow(page, panel, 'mail.acme.test')
  const destination = incidentRow(page, panel, 'Microsoft')

  // Both sides of both comparisons, over their own populations. The outside count
  // is what an operator disagrees with the inference with: 4 of 5 degraded is a
  // finding only while the rest of the pool is 1 of 6.
  await expect(figuresOf(signing)).toHaveText(['4 of 5', '1 of 6', '4.8×'])
  await expect(figuresOf(destination)).toHaveText(['4 of 7', '1 of 4', '2.3×'])

  // And the weaker one says it may be chance, where the stronger one does not —
  // hedging both would train an operator to discount every row, and hedging
  // neither makes 2.3× look like 4.8×.
  await expect(destination).toContainText(/read it as a hint/i)
  await expect(signing).not.toContainText(/read it as a hint/i)
})

// A correlation names the mailboxes it is about, because "4 mailboxes" leaves the
// operator to diff the pool by hand — the exact work this exists to remove. And
// it stays a correlation: nothing on the panel says the shared value is why.
test('a correlation names its members and claims no cause', async ({ page }) => {
  await serveOverview(page, CORRELATED_OVERVIEW)
  const panel = incidentsPanel(page)
  const signing = incidentRow(page, panel, 'mail.acme.test')

  await expect(signing.locator('[data-slot="incident-members"]')).toHaveText(
    'one@acme.test, two@acme.test, three@acme.test, four@acme.test',
  )
  await expect(signing).toContainText(/signed by the same DKIM d= domain/i)

  await expect(panel).toContainText(/does not say the shared value is why/i)
  await expect(panel).toContainText(/two dimensions can carry one underlying problem/i)
  await expect(panel).not.toContainText(/caused by|root cause|is broken|at fault|to blame/i)
  // Design §7's two reasons, on the screen with the rows.
  await expect(panel).toContainText(/no threshold, lane or promotion decision reads any of it/i)
  await expect(panel).toContainText(/steerable by whoever controls a mailbox domain's MX/i)
})

// An incident is a statement about SEVERAL mailboxes, so the one place it cannot
// live is on any single card. Physically above the list, not merely earlier in the
// DOM: an operator who reaches it after the cards has already diffed them by hand,
// and only a laid-out browser can tell the two apart.
test('the correlation panel is above the mailbox list, not buried in a card', async ({ page }) => {
  await serveOverview(page, CORRELATED_OVERVIEW)
  const panel = incidentsPanel(page)
  const list = page.locator('[data-slot="page-body"] > ul')

  await expect(list).toBeVisible()
  await expect(list.locator('[data-slot="warmup-incidents"]')).toHaveCount(0)

  const panelBox = await panel.boundingBox()
  const listBox = await list.boundingBox()
  if (!panelBox || !listBox) throw new Error('the panel and the mailbox list must both be laid out')
  expect(panelBox.y + panelBox.height).toBeLessThanOrEqual(listBox.y)
})

// The same empty array a perfectly healthy workspace gets, over a pool where five
// mailboxes are degrading. Rendered as a tidy "no incidents" it reads as
// reassurance; rendered as an apology it reads as a failure to compute. It is
// neither — and the count is the check on the whole thing: two of those five are
// degrading on their LANE, so a reading that watched only health_state would say
// three and quietly lose the withheld half of the pool.
test('an empty array over a degrading pool is an answer, not an empty state', async ({ page }) => {
  await serveOverview(page, UNATTRIBUTED_OVERVIEW)
  const panel = incidentsPanel(page)

  await expect(panel).toContainText('5 mailboxes are degrading')
  await expect(panel).toContainText(/no shared cause found/i)
  await expect(panel).toContainText(/work through them one at a time/i)
  // It names what was searched, so "no shared cause" is not read as a claim about
  // every possible cause.
  await expect(panel).toContainText(/no destination, signing domain, return path or sender domain/i)
  // And it is not the OTHER empty answer: this pool is not quiet.
  await expect(panel).not.toContainText(/no degradation in the pool/i)
  await expect(panel.locator('[data-slot="incident-value"]')).toHaveCount(0)
})

// A server that reports no incidents field at all has made no inference, so the
// panel says nothing — the default fixture in this file is exactly that server.
// "No shared cause found" here would claim a search nobody ran, which is the
// silent-fallback class the missing `lane` belonged to.
test('a server that does not report correlations shows no panel at all', async ({ page }) => {
  await expect(card(page, 'withheld@acme.test')).toBeVisible()

  await expect(incidentsPanel(page)).toHaveCount(0)
})

/* ----------------------------------------------------------- observer trust */

// The axis nothing acts on, which makes this panel the entire feature — and the
// two ways it lies if a copy rule is dropped. It must not read as a sanction (no
// mailbox was blocked, discounted or untrusted, and one of the two named here is
// a perfectly healthy participant), and it must not leave an operator believing
// their spam evidence was filtered out, because none of it was.
test('an observer verdict is a suspicion with its arithmetic, and excludes nothing', async ({ page }) => {
  await serveOverview(page, CORRELATED_OVERVIEW)
  const panel = page.getByRole('region', { name: 'Spam reporting outliers' })
  await expect(panel).toBeVisible()

  // Worst multiple first, and the one the pool cannot name is named by its id
  // rather than dropped — a verdict about nobody is worse than an ugly one.
  await expect(panel.locator('[data-slot="observer-mailbox"]')).toHaveText(['mb-gone', 'seven@partner.test'])
  // The cohort in an operator's language. `microsoft` is our contract's word, and
  // `other` is not a provider at all but a bag of them, which its phrasing says.
  await expect(panel.locator('[data-slot="observer-comparison"]')).toHaveText([
    'Compared with other mailboxes whose provider is neither Google nor Microsoft',
    'Compared with other Microsoft mailboxes',
  ])
  await expect(panel).not.toContainText('microsoft')
  await expect(panel).not.toContainText('unknown')

  // The arithmetic an operator disagrees with the verdict using: this mailbox's
  // rate over its own count, its peers' rate, and the multiple between them.
  // Scoped to the figure nodes, because each of the three still reads plausibly
  // when the labels and explanations around it are swept into the comparison.
  const microsoft = panel
    .locator('li')
    .filter({ has: page.locator('[data-slot="observer-mailbox"]', { hasText: 'seven@partner.test' }) })
  await expect(microsoft.locator('[data-slot="observer-stat"]')).toHaveText(['59 of 130 (45%)', '12%', '3.8×'])
  // A spotless cohort is scored against half a case rather than divided by zero,
  // so its multiple is not an exact ratio of 75% to 0%.
  const spotless = panel
    .locator('li')
    .filter({ has: page.locator('[data-slot="observer-mailbox"]', { hasText: 'mb-gone' }) })
  await expect(spotless.locator('[data-slot="observer-stat"]')).toHaveText(['30 of 40 (75%)', '0%', '60×'])
  await expect(spotless).toContainText(/rather than dividing by zero/i)
  // And it is not boilerplate under every row: the row above was divided by a real
  // peer rate. Verified against the revert — with the note hung on both rows the
  // assertion above still passed, so only the pair proves anything.
  await expect(microsoft).not.toContainText(/rather than dividing by zero/i)

  // Nothing happened to either mailbox, and the panel says so before the rows —
  // physically above them, since an operator who reads it afterwards has already
  // concluded their evidence was filtered. Only a laid-out browser can tell the
  // two apart.
  const note = panel.locator('[data-slot="observers-nothing-excluded"]')
  await expect(note).toContainText(/nothing is excluded/i)
  await expect(note).toContainText(/still counts as evidence/i)
  await expect(note).toContainText(/no health state, lane or promotion decision reads any of this/i)
  await expect(note).toContainText(/the peer comparison is gameable/i)
  const noteBox = await note.boundingBox()
  const rowBox = await microsoft.boundingBox()
  if (!noteBox || !rowBox) throw new Error('the note and the rows must both be laid out')
  expect(noteBox.y + noteBox.height).toBeLessThanOrEqual(rowBox.y)

  // Not a sanction, in any of the words that would state one.
  await expect(panel).not.toContainText(/untrusted|not trusted|hostile|discounted|blocked|removed|dropped/i)
  // And the healthy participant it names is still healthy on its own card: the
  // verdict is about what that mailbox REPORTED as a recipient, not how it sends.
  // Scoped to the mailbox list rather than through `card`, because that helper
  // matches any list item carrying the email — including this panel's own row,
  // which would make the assertion pass on the text it is meant to look past.
  const sevenCard = page.locator('[data-slot="page-body"] > ul > li').filter({ hasText: 'seven@partner.test' })
  await expect(sevenCard).toContainText(/healthy/i)
  await expect(sevenCard).not.toContainText(/reporting more spam/i)
})

/* ------------------------------------------------------------- sentinels */

/**
 * Designation is the one control on this screen that changes what a mailbox
 * RECEIVES — it starts taking warmup mail from degrading members that the rest of
 * the pool is shielded from — and the operator has to be told that BEFORE it
 * happens, not after.
 *
 * Only a browser can rule out the two ways that fails. A unit test can assert the
 * sentence exists; it cannot show the sentence was on screen before the request
 * left, and it cannot show the ordinary case reads as a label rather than as a
 * fault on a laid-out card. Both are the whole feature: peer-only is what a pool
 * with no sentinel produces on every row, and a warning tone there invents a
 * defect on most self-hosted installations at once.
 */
test('designating a sentinel shows what it costs before anything is written', async ({ page }) => {
  await serveOverview(page, SENTINEL_OVERVIEW)

  // The pool's arrangement, named — one sentinel out of three, and no advisory,
  // because the server said this pool is inside the advised share.
  const panel = page.getByRole('region', { name: 'Measurement sentinels' })
  await expect(panel).toBeVisible()
  await expect(panel.locator('[data-slot="sentinel-mailbox"]')).toHaveText(['reference@acme.test'])
  await expect(panel.locator('[data-slot="sentinel-advisory"]')).toHaveCount(0)
  // A label, not a penalty — said once above the rows that carry the labels.
  await expect(panel.locator('[data-slot="sentinel-gates-nothing"]')).toContainText(/never a penalty/i)

  const plain = page.locator('[data-slot="page-body"] > ul > li').filter({ hasText: 'plain@acme.test' })
  const label = plain.locator('[data-slot="evidence-confidence"]')
  await expect(label).toContainText('Peer-only')
  await expect(label).toContainText('gates nothing')
  // The ordinary case is not a fault. Asserted on the label node alone, because
  // the card around it legitimately carries a lane reason and a tabbed note that
  // would make a card-wide assertion pass on somebody else's words.
  await expect(label).not.toContainText(/insufficient|unreliable|weak|warning/i)

  // The corroborated row ships the count behind its label, as every other
  // inference on this screen ships its arithmetic.
  const watched = page.locator('[data-slot="page-body"] > ul > li').filter({ hasText: 'watched@acme.test' })
  await expect(watched.locator('[data-slot="evidence-confidence"]')).toContainText('9 from a sentinel')

  // The first click asks. It must not write.
  await page.getByRole('button', { name: 'Designate as sentinel for plain@acme.test' }).click()
  const prompt = plain.locator('[data-slot="sentinel-prompt"]')
  await expect(prompt).toContainText(/receive warmup mail from members that are degrading/i)
  await expect(prompt).toContainText(/shielded/i)
  // Containment outranks measurement: the prompt must not read as opening this
  // mailbox to quarantined senders too.
  await expect(prompt).toContainText(/quarantined or blocked mailbox is withheld/i)
  expect(sentinelWrites, 'asking must not write').toEqual([])

  await page.getByRole('button', { name: 'Designate as sentinel', exact: true }).click()

  await expect.poll(() => sentinelWrites.length).toBe(1)
  expect(sentinelWrites[0]).toBe('{"is_sentinel":true}')
})
