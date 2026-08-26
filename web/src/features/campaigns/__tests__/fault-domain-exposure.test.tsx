import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { FaultDomainExposure } from '../fault-domain-exposure'
import type { CampaignSender, CampaignSenderPool, FaultDomainShare } from '../api'

function share(overrides: Partial<FaultDomainShare> = {}): FaultDomainShare {
  return { domain: 'acme.test', assigned: 68, share: 0.68, ceiling: 0.6, over_budget: true, ...overrides }
}

function sender(overrides: Partial<CampaignSender> = {}): CampaignSender {
  return { mailbox_id: 'mb-1', email: 'one@acme.test', weight: 1, enabled: true, assigned_count: 68, ...overrides }
}

const OTHER = share({ domain: 'other.test', assigned: 32, share: 0.32, ceiling: 0.35, over_budget: false })

function pool(overrides: Partial<CampaignSenderPool> = {}): CampaignSenderPool {
  return {
    rotation_mode: 'weighted',
    senders: [sender(), sender({ mailbox_id: 'mb-2', email: 'two@other.test', assigned_count: 32 })],
    max_fault_domain_share: 0.6,
    fault_domain_shares: [share(), OTHER],
    ...overrides,
  }
}

function renderPanel(value: CampaignSenderPool | undefined = pool()) {
  return render(<FaultDomainExposure pool={value} />)
}

/**
 * The panel through the accessibility tree, which is how it is reached in a
 * browser too — a named region, not a class or a test id.
 */
function region(): HTMLElement {
  return screen.getByRole('region', { name: /domain concentration/i })
}

/** The panel as an operator reads it: all of its text, in one string. */
function panelText(): string {
  return document.querySelector('[data-slot="fault-domain-exposure"]')?.textContent ?? ''
}

/**
 * Each row's rendered nodes, scoped to the VALUE node alone.
 *
 * Scoped that tightly deliberately: every row carries a domain, two percentages,
 * a count, a status word and up to two explanatory sentences, so a figure that
 * silently collapsed still reads plausibly when the surrounding label text is
 * swept into the comparison.
 */
function slots(name: string): string[] {
  return [...document.querySelectorAll(`[data-slot="${name}"]`)].map((node) => node.textContent ?? '')
}

const domains = () => slots('exposure-domain')
const shares = () => slots('exposure-share')
const ceilings = () => slots('exposure-ceiling')
const assigned = () => slots('exposure-assigned')
const statuses = () => slots('status-pill')
const details = () => slots('exposure-detail')
const tightened = () => slots('exposure-tightened')

/* ------------------------------------------------------------------ the table */

test('each domain renders its own share, its own ceiling and its contacts', () => {
  renderPanel()

  expect(region()).toBeInTheDocument()
  expect(domains()).toEqual(['acme.test', 'other.test'])
  expect(shares()).toEqual(['68%', '32%'])
  expect(ceilings()).toEqual(['60%', '35%'])
  expect(assigned()).toEqual(['68 contacts assigned', '32 contacts assigned'])
})

/**
 * The rule this panel is most likely to lose, and the one that makes it read as
 * broken arithmetic when it is lost: the ceiling is PER DOMAIN. A degrading
 * domain is held below the campaign's limit, so the smaller share is the one
 * over budget.
 */
test('a domain at 25% is over budget while one at 55% is not, and the row says why', () => {
  renderPanel(
    pool({
      fault_domain_shares: [
        share({ domain: 'degrading.test', assigned: 25, share: 0.25, ceiling: 0.2, over_budget: true }),
        share({ domain: 'steady.test', assigned: 55, share: 0.55, ceiling: 0.6, over_budget: false }),
      ],
    }),
  )

  expect(shares()).toEqual(['25%', '55%'])
  expect(ceilings()).toEqual(['20%', '60%'])
  expect(statuses()).toEqual(['Over budget', 'Within budget'])
  // Exactly one row was tightened, and it names both numbers so the comparison
  // between the two rows is legible rather than contradictory.
  expect(tightened()).toHaveLength(1)
  expect(tightened()[0]).toMatch(/Held to 20% rather than the campaign's 60%/)
  expect(tightened()[0]).toMatch(/degrading/)
})

test('the panel states up front that each domain has its own ceiling', () => {
  renderPanel()
  expect(panelText()).toMatch(/Each domain is measured against its OWN ceiling/)
  expect(panelText()).toMatch(/25% can be over budget while another at 55% is not/)
})

/* ----------------------------------------------------- over budget is not a block */

test('over budget is stated as a shift in who sends next, never as a stoppage', () => {
  renderPanel()

  expect(statuses()).toEqual(['Over budget', 'Within budget'])
  expect(details()).toHaveLength(1)
  expect(details()[0]).toMatch(/new contacts go to a mailbox on another domain/i)
  expect(details()[0]).toMatch(/Nothing is paused and no send is withheld/i)
  expect(panelText()).toMatch(/none of this withholds a send, pauses a mailbox or slows the campaign down/i)
  // The vocabulary of a failure, none of which happened.
  expect(panelText()).not.toMatch(/blocked|violation|exceeded|not allowed|too many/i)
})

/* --------------------------------------------------------------- one domain */

test('a pool on one domain reads as a dependency to fix, not a budget it broke', () => {
  renderPanel(
    pool({
      senders: [sender({ assigned_count: 100 })],
      fault_domain_shares: [share({ assigned: 100, share: 1, ceiling: 0.6, over_budget: true })],
    }),
  )

  // Not a warning the operator cannot act on: the flag is true and the figures
  // plainly are over, but a budget with nowhere to shift to judged nothing.
  expect(statuses()).toEqual(['Only domain'])
  expect(panelText()).not.toMatch(/Over budget/)
  // Nothing is hidden — the arithmetic that made the flag true is still on screen.
  expect(shares()).toEqual(['100%'])
  expect(ceilings()).toEqual(['60%'])

  expect(slots('exposure-sole')[0]).toMatch(/depends entirely on acme\.test/)
  expect(slots('exposure-sole')[0]).toMatch(/does not apply/)
  expect(slots('exposure-sole')[0]).toMatch(/Connect a mailbox on another domain to spread the risk/)
})

/* ------------------------------------------------------------ the two empties */

test('no assignments yet is "nothing to measure", and says it is not "balanced"', () => {
  renderPanel(pool({ senders: [sender({ assigned_count: 0 })], fault_domain_shares: [] }))

  expect(panelText()).toMatch(/no concentration to measure/i)
  expect(panelText()).toMatch(/Not the same as balanced/i)
  expect(domains()).toEqual([])
})

test('assignments with no shared domain is the other answer, and names the reason', () => {
  renderPanel(pool({ senders: [sender({ assigned_count: 40 })], fault_domain_shares: [] }))

  expect(panelText()).toMatch(/No shared domain to report/i)
  expect(panelText()).toMatch(/gmail\.com/)
  expect(panelText()).toMatch(/do not share a fate/i)
  expect(panelText()).not.toMatch(/no concentration to measure/i)
})

/* ------------------------------------------------------------ not exhaustive */

test('rows that do not add up to the pool are not presented as a full breakdown', () => {
  renderPanel(
    pool({
      fault_domain_shares: [
        share({ assigned: 50, share: 0.5, over_budget: false }),
        share({ domain: 'other.test', assigned: 32, share: 0.32, ceiling: 0.6, over_budget: false }),
      ],
    }),
  )

  expect(slots('exposure-uncovered')[0]).toMatch(/cover 82% of the contacts assigned/)
  expect(slots('exposure-uncovered')[0]).toMatch(/not as a breakdown of the whole pool/)
})

test('rows that do add up carry no shortfall note', () => {
  renderPanel()
  expect(slots('exposure-uncovered')).toEqual([])
})

/* ---------------------------------------------------------------- the nouns */

test('the count is contacts assigned — this panel never calls it mail sent', () => {
  renderPanel()
  expect(assigned()).toEqual(['68 contacts assigned', '32 contacts assigned'])
  expect(panelText()).not.toMatch(/\bsent\b/i)
})

/* -------------------------------------------------------------- the silences */

test('no pool renders nothing at all', () => {
  const { container } = render(<FaultDomainExposure pool={undefined} />)
  expect(container).toBeEmptyDOMElement()
})

test('a campaign with no senders renders nothing — the panel above already said so', () => {
  const { container } = renderPanel(pool({ senders: [], fault_domain_shares: [] }))
  expect(container).toBeEmptyDOMElement()
})

test('a server predating exposure reporting renders nothing, not a clean pool', () => {
  const legacy = { rotation_mode: 'weighted', senders: [sender()] } as unknown as CampaignSenderPool
  const { container } = renderPanel(legacy)
  expect(container).toBeEmptyDOMElement()
})

/* ------------------------------------------------------------- accessibility */

test('status is a word, and the meter beside it is not a second announcement', () => {
  renderPanel()

  // Colour is never the only signal: each row's state is readable text.
  expect(statuses()).toEqual(['Over budget', 'Within budget'])
  // One meter per row, every one of them out of the accessibility tree: it
  // repeats two figures that are already text, less precisely than the text does.
  const meters = [...region().querySelectorAll('[data-slot="exposure-meter"]')]
  expect(meters).toHaveLength(2)
  expect(meters.every((meter) => meter.getAttribute('aria-hidden') === 'true')).toBe(true)
  // And it carries no text of its own to be read out.
  expect(meters.map((meter) => meter.textContent)).toEqual(['', ''])
})

test('a row the server sent no share for renders no meter rather than an empty track', () => {
  renderPanel(pool({ fault_domain_shares: [share({ share: Number.NaN, ceiling: Number.NaN })] }))

  expect(shares()).toEqual(['Not stated'])
  expect(region().querySelectorAll('[data-slot="exposure-meter"]')).toHaveLength(0)
})
