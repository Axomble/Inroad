import { describe, expect, test } from 'vitest'
import type { SendingDomain } from '@/store/api'
import {
  dkimCheck,
  dmarcCheck,
  domainChecks,
  domainStateLabel,
  domainStateTone,
  domainSummary,
  lastCheckedLabel,
  listErrorMessage,
  mailboxCountLabel,
  recheckErrorMessage,
  spfCheck,
} from '../domain-auth'

const CHECKED_AT = '2026-08-03T10:00:00.000Z'

function domain(overrides: Partial<SendingDomain> = {}): SendingDomain {
  return {
    domain: 'acme.com',
    state: 'passing',
    spf: { found: true, record: 'v=spf1 include:_spf.google.com ~all' },
    dmarc: { found: true, policy: 'reject' },
    dkim: { found: true, selector: 'google' },
    mailbox_count: 3,
    checked_at: CHECKED_AT,
    ...overrides,
  }
}

describe('spfCheck', () => {
  test('a published record passes and shows the record itself', () => {
    const check = spfCheck(domain())
    expect(check.verdict).toBe('pass')
    expect(check.status).toBe('Set up')
    expect(check.detail).toContain('v=spf1 include:_spf.google.com ~all')
  })

  test('a missing record names the TXT record to add, not just that it is wrong', () => {
    const check = spfCheck(domain({ state: 'failing', spf: { found: false } }))
    expect(check.verdict).toBe('attention')
    expect(check.tone).toBe('failing')
    expect(check.detail).toMatch(/TXT record on acme\.com/)
    expect(check.detail).toMatch(/v=spf1/)
  })

  test('a missing record under an unknown state reads as could-not-check, not missing', () => {
    const check = spfCheck(domain({ state: 'unknown', spf: { found: false } }))
    expect(check.verdict).toBe('unknown')
    expect(check.tone).toBe('draft')
    expect(check.status).toBe("Couldn't check")
    expect(check.detail).toMatch(/not a problem with your setup/)
  })

  test('a never-checked domain says so rather than reporting a lookup failure', () => {
    const check = spfCheck(domain({ state: 'unknown', spf: { found: false }, checked_at: null }))
    expect(check.status).toBe('Not checked yet')
    expect(check.detail).toMatch(/hasn't been checked yet/)
  })

  test('a record found despite an unknown domain state still passes', () => {
    // A hit is positive evidence; only the *other* lookup failed.
    expect(spfCheck(domain({ state: 'unknown' })).verdict).toBe('pass')
  })
})

describe('dmarcCheck', () => {
  test.each(['quarantine', 'reject'] as const)('p=%s reads as enforcing', (policy) => {
    const check = dmarcCheck(domain({ dmarc: { found: true, policy } }))
    expect(check.verdict).toBe('pass')
    expect(check.status).toBe(`Enforcing (p=${policy})`)
  })

  test('p=none reads as report-only, never as a plain pass', () => {
    const check = dmarcCheck(domain({ dmarc: { found: true, policy: 'none' } }))
    expect(check.verdict).toBe('monitoring')
    expect(check.status).toBe('Report only')
    expect(check.tone).not.toBe('running')
    expect(check.detail).toMatch(/only collects reports/)
    expect(check.detail).toMatch(/aren't asked to do anything about suspicious mail/)
  })

  test('a published record with no p= tag is monitoring too, and says why', () => {
    const check = dmarcCheck(domain({ dmarc: { found: true, policy: '' } }))
    expect(check.verdict).toBe('monitoring')
    expect(check.detail).toMatch(/no p= tag/)
  })

  test('a missing record names the _dmarc host to add', () => {
    const check = dmarcCheck(domain({ state: 'failing', dmarc: { found: false } }))
    expect(check.verdict).toBe('attention')
    expect(check.detail).toMatch(/_dmarc\.acme\.com/)
    expect(check.detail).toMatch(/v=DMARC1; p=none/)
  })

  test('a missing record under an unknown state is could-not-check', () => {
    expect(dmarcCheck(domain({ state: 'unknown', dmarc: { found: false } })).verdict).toBe('unknown')
  })
})

describe('dkimCheck', () => {
  test('a detected selector passes and names the host it was found at', () => {
    const check = dkimCheck(domain())
    expect(check.verdict).toBe('pass')
    expect(check.detail).toContain('google._domainkey.acme.com')
  })

  test('not detected is advisory, never a failure, and explains the probe limit', () => {
    const check = dkimCheck(domain({ dkim: { found: false } }))
    expect(check.verdict).toBe('advisory')
    expect(check.status).toBe('Not detected')
    // The whole point: nothing here may read as broken or missing.
    expect(check.tone).not.toBe('failing')
    expect(check.status).not.toMatch(/missing/i)
    expect(check.detail).toMatch(/most common DKIM setups/)
    expect(check.detail).toMatch(/never counts against your domain/)
  })

  test('DKIM stays advisory even when the domain state is unknown', () => {
    // DKIM is not an authoritative lookup, so a resolver failure elsewhere must
    // not turn it into a "couldn't check" and imply it might have been fine.
    expect(dkimCheck(domain({ state: 'unknown', dkim: { found: false } })).verdict).toBe('advisory')
  })
})

describe('domainChecks', () => {
  test('returns SPF, DKIM, DMARC in reading order', () => {
    expect(domainChecks(domain()).map((c) => c.id)).toEqual(['spf', 'dkim', 'dmarc'])
  })

  test('a domain passing on SPF + DMARC has no attention verdict even with no DKIM', () => {
    const checks = domainChecks(domain({ dkim: { found: false } }))
    expect(checks.some((c) => c.verdict === 'attention')).toBe(false)
  })
})

describe('domainStateLabel / domainStateTone', () => {
  test('passing reads as looking good', () => {
    expect(domainStateLabel(domain())).toBe('Looks good')
    expect(domainStateTone(domain())).toBe('running')
  })

  test('failing asks for action', () => {
    const failing = domain({ state: 'failing', spf: { found: false } })
    expect(domainStateLabel(failing)).toBe('Needs a fix')
    expect(domainStateTone(failing)).toBe('failing')
  })

  test('unknown is faint and distinguishes never-checked from a failed lookup', () => {
    const failedLookup = domain({ state: 'unknown' })
    const neverChecked = domain({ state: 'unknown', checked_at: null })
    expect(domainStateLabel(failedLookup)).toBe("Couldn't check")
    expect(domainStateLabel(neverChecked)).toBe('Not checked yet')
    // Never red — `unknown` is not a verdict.
    expect(domainStateTone(failedLookup)).toBe('draft')
    expect(domainStateTone(neverChecked)).toBe('draft')
  })
})

describe('domainSummary', () => {
  test('an enforcing domain says both records are in place', () => {
    expect(domainSummary(domain())).toMatch(/set up and protecting/)
  })

  test('p=none is called out as reporting, not enforcing', () => {
    const summary = domainSummary(domain({ dmarc: { found: true, policy: 'none' } }))
    expect(summary).toMatch(/report-only mode/)
    expect(summary).toMatch(/without blocking anything/)
  })

  test('a failing domain lists exactly the records to add', () => {
    const both = domainSummary(domain({ state: 'failing', spf: { found: false }, dmarc: { found: false } }))
    expect(both).toBe(
      'Add an SPF record and a DMARC record in your DNS settings so inbox providers trust email from this domain — the exact records to add are below.',
    )

    const spfOnly = domainSummary(domain({ state: 'failing', spf: { found: false } }))
    expect(spfOnly).toBe(
      'Add an SPF record in your DNS settings so inbox providers trust email from this domain — the exact records to add are below.',
    )
    expect(spfOnly).not.toMatch(/DMARC/)
  })

  test('a failing domain never blames DKIM', () => {
    const summary = domainSummary(domain({ state: 'failing', spf: { found: false }, dkim: { found: false } }))
    expect(summary).not.toMatch(/DKIM/i)
  })

  test('unknown separates "never checked" from a lookup that did not answer', () => {
    expect(domainSummary(domain({ state: 'unknown', checked_at: null }))).toMatch(/Not checked yet/)
    const failed = domainSummary(domain({ state: 'unknown' }))
    expect(failed).toMatch(/problem with the check itself, not with your setup/)
  })
})

describe('lastCheckedLabel', () => {
  test('never-checked says so instead of showing an empty timestamp', () => {
    expect(lastCheckedLabel(null)).toBe('Not checked yet')
    expect(lastCheckedLabel(undefined)).toBe('Not checked yet')
  })

  test('a timestamp renders relative to the injected now', () => {
    const now = new Date(CHECKED_AT).getTime() + 3 * 3_600_000
    expect(lastCheckedLabel(CHECKED_AT, now)).toBe('Checked 3 hours ago')
  })
})

describe('mailboxCountLabel', () => {
  test('singular and plural', () => {
    expect(mailboxCountLabel(1)).toBe('1 mailbox')
    expect(mailboxCountLabel(0)).toBe('0 mailboxes')
    expect(mailboxCountLabel(4)).toBe('4 mailboxes')
  })
})

describe('recheckErrorMessage', () => {
  test('404 explains the workspace no longer sends from the domain', () => {
    expect(recheckErrorMessage({ status: 404, data: {} }, 'acme.com')).toMatch(
      /No mailbox in this workspace sends from acme\.com/,
    )
  })

  test('429 asks for a pause rather than implying a DNS problem', () => {
    expect(recheckErrorMessage({ status: 429, data: {} }, 'acme.com')).toMatch(/Too many rechecks/)
  })

  test("the server's own reason is preferred, and the copy never implies a verdict", () => {
    const message = recheckErrorMessage({ status: 502, data: { error: 'resolver timeout' } }, 'acme.com')
    expect(message).toContain('resolver timeout')
    expect(message).toMatch(/records are unaffected/)
  })

  test('a transport error (no HTTP status) still gets human copy', () => {
    const message = recheckErrorMessage({ status: 'FETCH_ERROR', error: 'boom' }, 'acme.com')
    expect(message).toBe("Couldn't check acme.com right now. Your DNS records are unaffected — try again.")
  })
})

describe('listErrorMessage', () => {
  test('includes the status when there is one and disclaims any DNS verdict', () => {
    expect(listErrorMessage({ status: 500, data: {} })).toBe(
      "Couldn't load the domain checks (500). This doesn't mean anything is wrong with your domains — try again.",
    )
  })

  test('omits the status for a transport error', () => {
    expect(listErrorMessage({ status: 'FETCH_ERROR', error: 'boom' })).toBe(
      "Couldn't load the domain checks. This doesn't mean anything is wrong with your domains — try again.",
    )
  })
})
