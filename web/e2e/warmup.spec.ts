import { expect, test, type Page, type Route } from '@playwright/test'

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

async function mockApi(page: Page) {
  transitionRequests = 0
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
    if (path.endsWith('/warmup/overview')) return route.fulfill(json(OVERVIEW))

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
      return route.fulfill(json(OVERVIEW.mailboxes.map((m) => ({
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
