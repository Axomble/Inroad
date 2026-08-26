import { describe, expect, test } from 'vitest'
import { exposureReading } from '../exposure-copy'
import type { CampaignSender, CampaignSenderPool, FaultDomainShare } from '../api'

function share(overrides: Partial<FaultDomainShare> = {}): FaultDomainShare {
  return { domain: 'acme.test', assigned: 68, share: 0.68, ceiling: 0.6, over_budget: true, ...overrides }
}

function sender(overrides: Partial<CampaignSender> = {}): CampaignSender {
  return {
    mailbox_id: 'mb-1',
    email: 'one@acme.test',
    weight: 1,
    enabled: true,
    assigned_count: 68,
    ...overrides,
  }
}

function pool(overrides: Partial<CampaignSenderPool> = {}): CampaignSenderPool {
  return {
    rotation_mode: 'weighted',
    senders: [sender()],
    max_fault_domain_share: 0.6,
    fault_domain_shares: [share()],
    ...overrides,
  }
}

/** The rows of a reading that produced any, or a failure that names what came back instead. */
function domainsOf(reading: ReturnType<typeof exposureReading>) {
  if (reading.kind !== 'measured') throw new Error(`expected a measured reading, got ${reading.kind}`)
  return reading.domains
}

describe('exposureReading', () => {
  /* ------------------------------------------------------------ the absences */

  test('no pool at all is silence, not a measurement', () => {
    expect(exposureReading(undefined).kind).toBe('unreported')
  })

  // The fields are required on a conforming response, so an absent array is a
  // server predating exposure reporting. "No concentration" would report a
  // search nobody ran.
  test('a server that does not report exposure is silence too', () => {
    const legacy = { rotation_mode: 'weighted', senders: [sender()] } as unknown as CampaignSenderPool
    expect(exposureReading(legacy).kind).toBe('unreported')
  })

  test('a campaign with no senders says nothing — the panel above it already has', () => {
    expect(exposureReading(pool({ senders: [], fault_domain_shares: [] })).kind).toBe('unreported')
  })

  test('no contacts assigned yet is "nothing to measure", explicitly not "balanced"', () => {
    const reading = exposureReading(
      pool({ senders: [sender({ assigned_count: 0 })], fault_domain_shares: [] }),
    )
    expect(reading.kind).toBe('unassigned')
    if (reading.kind !== 'unassigned') throw new Error('unreachable')
    expect(reading.message).toMatch(/no concentration to measure/i)
    expect(reading.message).toMatch(/Not the same as balanced/i)
  })

  // The same empty array, a different fact: there ARE assignments, and nothing
  // among them can fail together.
  test('contacts assigned with nothing groupable is a different answer, and says why', () => {
    const reading = exposureReading(pool({ senders: [sender({ assigned_count: 40 })], fault_domain_shares: [] }))
    expect(reading.kind).toBe('ungrouped')
    if (reading.kind !== 'ungrouped') throw new Error('unreachable')
    expect(reading.message).toMatch(/No shared domain to report/i)
    expect(reading.message).toMatch(/gmail\.com/)
    expect(reading.message).toMatch(/do not share a fate/i)
  })

  /* ----------------------------------------------------------- the ceilings */

  // The rule the whole module exists for: each row is judged against its own
  // ceiling, so the low share can be the over-budget one.
  test('a domain at 25% is over budget while one at 55% is not, each against its own ceiling', () => {
    const domains = domainsOf(
      exposureReading(
        pool({
          fault_domain_shares: [
            share({ domain: 'degrading.test', assigned: 25, share: 0.25, ceiling: 0.2, over_budget: true }),
            share({ domain: 'steady.test', assigned: 55, share: 0.55, ceiling: 0.6, over_budget: false }),
          ],
        }),
      ),
    )

    expect(domains.map((d) => d.share)).toEqual(['25%', '55%'])
    expect(domains.map((d) => d.ceiling)).toEqual(['20%', '60%'])
    expect(domains.map((d) => d.status)).toEqual(['Over budget', 'Within budget'])
  })

  test("a tightened ceiling names the campaign's and the reason it was lowered", () => {
    const domains = domainsOf(
      exposureReading(pool({ fault_domain_shares: [share({ ceiling: 0.35, share: 0.4 })] })),
    )
    expect(domains[0]?.tightened).toMatch(/Held to 35% rather than the campaign's 60%/)
    expect(domains[0]?.tightened).toMatch(/degrading/)
  })

  test('a domain held to the campaign ceiling carries no lowered-ceiling note', () => {
    expect(domainsOf(exposureReading(pool())).map((d) => d.tightened)).toEqual([null])
  })

  /* ------------------------------------------------------- over is not a block */

  test('over budget explains what it did, which is shift the next contact and nothing else', () => {
    const domains = domainsOf(
      exposureReading(
        pool({
          fault_domain_shares: [
            share({ share: 0.68, over_budget: true }),
            share({ domain: 'other.test', assigned: 32, share: 0.32, ceiling: 0.6, over_budget: false }),
          ],
        }),
      ),
    )
    expect(domains[0]?.detail).toMatch(/new contacts go to a mailbox on another domain/i)
    expect(domains[0]?.detail).toMatch(/Nothing is paused and no send is withheld/i)
    // A row inside its ceiling needs no gloss: the figures and the word say it.
    expect(domains[1]?.detail).toBeNull()
  })

  /* --------------------------------------------------------- the single domain */

  test('one domain carrying everything is a dependency, not a violation', () => {
    const reading = exposureReading(
      pool({ fault_domain_shares: [share({ assigned: 100, share: 1, ceiling: 0.6, over_budget: true })] }),
    )
    const domains = domainsOf(reading)

    expect(domains[0]?.status).toBe('Only domain')
    expect(domains[0]?.tone).toBe('inapplicable')
    // The arithmetic is still on screen — nothing is hidden, it is read differently.
    expect(domains[0]?.share).toBe('100%')
    expect(domains[0]?.ceiling).toBe('60%')
    if (reading.kind !== 'measured') throw new Error('unreachable')
    expect(reading.soleNote).toMatch(/depends entirely on acme\.test/)
    expect(reading.soleNote).toMatch(/does not apply/)
    expect(reading.soleNote).toMatch(/Connect a mailbox on another domain/)
  })

  // Float division over a three-mailbox pool does not land on 1 exactly.
  test('a share one ulp short of the whole is still the only domain', () => {
    const reading = exposureReading(
      pool({ fault_domain_shares: [share({ share: 0.9999999999999999, over_budget: true })] }),
    )
    expect(domainsOf(reading)[0]?.status).toBe('Only domain')
  })

  // One row is not automatically the sole-domain case: the rest of the pool can
  // be consumer mailboxes, which ARE an alternative the rotation can shift to.
  test('a lone row that does not cover the pool is an ordinary over-budget row', () => {
    const reading = exposureReading(
      pool({ fault_domain_shares: [share({ assigned: 60, share: 0.6, ceiling: 0.5, over_budget: true })] }),
    )
    expect(domainsOf(reading)[0]?.status).toBe('Over budget')
    if (reading.kind !== 'measured') throw new Error('unreachable')
    expect(reading.soleNote).toBeNull()
    expect(reading.uncovered).toMatch(/cover 60% of the contacts assigned/)
  })

  /* ------------------------------------------------------------ not exhaustive */

  test('rows that do not add up to the pool say so, and say why', () => {
    const reading = exposureReading(
      pool({
        fault_domain_shares: [
          share({ assigned: 50, share: 0.5, over_budget: false }),
          share({ domain: 'other.test', assigned: 32, share: 0.32, ceiling: 0.6, over_budget: false }),
        ],
      }),
    )
    if (reading.kind !== 'measured') throw new Error('unreachable')
    expect(reading.uncovered).toMatch(/cover 82% of the contacts assigned/)
    expect(reading.uncovered).toMatch(/gmail\.com/)
    expect(reading.uncovered).toMatch(/not as a breakdown of the whole pool/)
  })

  test('rows that do add up carry no shortfall note', () => {
    const reading = exposureReading(
      pool({
        fault_domain_shares: [
          share({ share: 0.68, over_budget: true }),
          share({ domain: 'other.test', assigned: 32, share: 0.32, ceiling: 0.35, over_budget: false }),
        ],
      }),
    )
    if (reading.kind !== 'measured') throw new Error('unreachable')
    expect(reading.uncovered).toBeNull()
  })

  /* ---------------------------------------------------------------- the figures */

  test('assigned counts contacts, and one of them is singular', () => {
    const domains = domainsOf(
      exposureReading(
        pool({
          fault_domain_shares: [
            share({ assigned: 1, share: 0.5, ceiling: 0.6, over_budget: false }),
            share({ domain: 'other.test', assigned: 1200, share: 0.5, ceiling: 0.6, over_budget: false }),
          ],
        }),
      ),
    )
    expect(domains.map((d) => d.assigned)).toEqual(['1 contact assigned', '1,200 contacts assigned'])
  })

  // A real concentration rounded to a confident 0% or 100% is the false-clean
  // reading every rate on this codebase refuses to print.
  test('a share too small to round to a percent is not printed as zero', () => {
    const domains = domainsOf(
      exposureReading(pool({ fault_domain_shares: [share({ share: 0.002, ceiling: 0.6, over_budget: false })] })),
    )
    expect(domains[0]?.share).toBe('<1%')
  })

  test('a share just short of the whole is not printed as the whole', () => {
    const reading = exposureReading(
      pool({
        fault_domain_shares: [
          share({ share: 0.997, over_budget: true }),
          share({ domain: 'other.test', assigned: 1, share: 0.003, ceiling: 0.6, over_budget: false }),
        ],
      }),
    )
    expect(domainsOf(reading)[0]?.share).toBe('>99%')
  })

  test('a share the server did not state is words, never NaN, and drops the meter', () => {
    const broken = share({ share: Number.NaN, ceiling: Number.NaN })
    const reading = exposureReading(pool({ fault_domain_shares: [broken] }))
    const domains = domainsOf(reading)

    expect(domains[0]?.share).toBe('Not stated')
    expect(domains[0]?.ceiling).toBe('Not stated')
    expect(domains[0]?.meter).toBeNull()
    expect(domains[0]?.tightened).toBeNull()
    // A shortfall totalled over a missing figure would name our own gap as the
    // pool's, so it is not claimed at all.
    if (reading.kind !== 'measured') throw new Error('unreachable')
    expect(reading.uncovered).toBeNull()
  })

  test('the server order is kept — it reports worst first, and this must not re-sort', () => {
    const domains = domainsOf(
      exposureReading(
        pool({
          fault_domain_shares: [
            share({ domain: 'big.test', assigned: 60, share: 0.6, ceiling: 0.6, over_budget: false }),
            share({ domain: 'small.test', assigned: 15, share: 0.15, ceiling: 0.1, over_budget: true }),
            share({ domain: 'mid.test', assigned: 25, share: 0.25, ceiling: 0.6, over_budget: false }),
          ],
        }),
      ),
    )
    expect(domains.map((d) => d.domain)).toEqual(['big.test', 'small.test', 'mid.test'])
  })
})
