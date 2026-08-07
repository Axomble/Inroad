import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { Mailbox, SendingDomain } from '@/store/api'
import { DomainAuthHeader, DomainAuthNotice } from './domain-auth-header'
// Importing the feature api registers the sending-domain tag wiring on the
// shared endpoints registry, so a recheck invalidates the list.
import { useListSendingDomainsQuery } from './api'
import { groupMailboxesByDomain } from './domain-group'

const jsonHeaders = { 'content-type': 'application/json' }

const PASSING: SendingDomain = {
  domain: 'acme.com',
  state: 'passing',
  spf: { found: true, record: 'v=spf1 include:_spf.google.com ~all' },
  dmarc: { found: true, policy: 'reject' },
  dkim: { found: true, selector: 'google' },
  mailbox_count: 3,
  checked_at: new Date(Date.now() - 3_600_000).toISOString(),
}

const FAILING: SendingDomain = {
  ...PASSING,
  domain: 'startup.io',
  state: 'failing',
  spf: { found: false },
  dmarc: { found: false },
  dkim: { found: false },
  mailbox_count: 1,
}

const UNKNOWN: SendingDomain = {
  ...PASSING,
  domain: 'newco.dev',
  state: 'unknown',
  spf: { found: false },
  dmarc: { found: false },
  dkim: { found: false },
  checked_at: null,
}

/** Three mailboxes on whichever domain is under test, so counts read as real. */
const mailboxesOn = (domain: string): Mailbox[] =>
  [1, 2, 3].map((n) => ({ id: `m-${n}`, email: `sender${n}@${domain}`, status: 'active' }))

/**
 * Mirrors how the page wires this component: the domains query feeds the group,
 * so an invalidating recheck re-renders the heading exactly as it does in situ.
 */
function Harness({ mailboxes }: { mailboxes: Mailbox[] }) {
  const { data, isLoading, error } = useListSendingDomainsQuery()
  if (error) return <DomainAuthNotice error={error} />
  return (
    <>
      {groupMailboxesByDomain(mailboxes, data ?? []).map((group) => (
        <DomainAuthHeader key={group.domain} group={group} isLoadingAuth={isLoading} />
      ))}
    </>
  )
}

/**
 * Stubs `GET /sending-domains` with each response in `pages` in turn (the last
 * one repeating), and `POST /sending-domains/{domain}/check` with `checkStatus`.
 * Sequencing the list lets a recheck round-trip assert the heading updated.
 */
function stubDomains({
  pages,
  checkStatus = 200,
  checkBody,
}: {
  pages: SendingDomain[][] | { status: number }
  checkStatus?: number
  checkBody?: unknown
}) {
  let listCall = 0
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const req = input as Request
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: jsonHeaders })

    if (req.url.includes('/check')) {
      return checkStatus === 200 ? json(PASSING) : json(checkBody ?? { error: 'nope' }, checkStatus)
    }
    if (!Array.isArray(pages)) return json({ error: 'boom' }, pages.status)
    const page = pages[Math.min(listCall, pages.length - 1)]
    listCall += 1
    return json(page)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

/** Expands the disclosure so the per-record sentences are on screen. */
async function showRecords(domain: string) {
  fireEvent.click(await screen.findByRole('button', { name: `Show DNS records for ${domain}` }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('DomainAuthHeader', () => {
  test('collapsed, it answers the domain, its verdict, its records, and its coverage on one line', async () => {
    stubDomains({ pages: [[PASSING]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)

    expect(await screen.findByText('acme.com')).toBeInTheDocument()
    expect(await screen.findByText('Authenticated')).toBeInTheDocument()
    expect(screen.getByText(/3 mailboxes · Checked 1 hour ago/)).toBeInTheDocument()
    // The compact record tokens, not the full sentences: those stay collapsed.
    expect(screen.getByText('SPF ok')).toBeInTheDocument()
    expect(screen.getByText('DKIM ok')).toBeInTheDocument()
    expect(screen.getByText('DMARC ok')).toBeInTheDocument()
    expect(screen.queryByText(/Published at the domain apex/)).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('the disclosure explains every record, passing ones included', async () => {
    stubDomains({ pages: [[PASSING]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)
    await showRecords('acme.com')

    expect(screen.getByText('Enforcing (p=reject).')).toBeInTheDocument()
    expect(screen.getByText(/Published at the domain apex: v=spf1/)).toBeInTheDocument()
    expect(screen.getByText(/signing key is published at google\._domainkey\.acme\.com/)).toBeInTheDocument()
    // And it closes again, so a long-lived page doesn't accumulate open blocks.
    fireEvent.click(screen.getByRole('button', { name: 'Hide DNS records for acme.com' }))
    expect(screen.queryByText('Enforcing (p=reject).')).not.toBeInTheDocument()
  })

  test('a failing domain names the DNS records to add', async () => {
    stubDomains({ pages: [[FAILING]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('startup.io')} />)

    expect(await screen.findByText('startup.io')).toBeInTheDocument()
    expect(await screen.findByText('Action needed')).toBeInTheDocument()
    expect(screen.getByText('SPF missing')).toBeInTheDocument()
    expect(screen.getByText('DMARC missing')).toBeInTheDocument()

    await showRecords('startup.io')
    expect(
      screen.getByText(/Add an SPF TXT record at the apex and a DMARC TXT record at _dmarc\.startup\.io/),
    ).toBeInTheDocument()
    expect(screen.getByText(/starting v=spf1/)).toBeInTheDocument()
    expect(screen.getByText(/_dmarc\.startup\.io starting v=DMARC1; p=none/)).toBeInTheDocument()
  })

  test('DKIM not detected reads as advisory, not as a broken record', async () => {
    stubDomains({ pages: [[{ ...PASSING, dkim: { found: false } }]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)

    expect(await screen.findByText('DKIM no signal')).toBeInTheDocument()
    // The domain is still authenticated, and nothing about DKIM is a failure.
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
    expect(screen.queryByText(/DKIM.*missing/i)).not.toBeInTheDocument()

    await showRecords('acme.com')
    expect(screen.getByText(/selectors can't be discovered from DNS/)).not.toHaveClass('text-danger')
  })

  test('DMARC p=none reads as monitoring, not as protection', async () => {
    stubDomains({ pages: [[{ ...PASSING, dmarc: { found: true, policy: 'none' } }]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)

    expect(await screen.findByText('DMARC monitor')).toBeInTheDocument()
    await showRecords('acme.com')
    expect(screen.getByText(/DMARC is monitoring only/)).toBeInTheDocument()
    expect(screen.getByText(/only collects reports/)).toBeInTheDocument()
    expect(screen.queryByText(/Enforcing/)).not.toBeInTheDocument()
  })

  test('an unchecked domain reads as not checked, never as a failure', async () => {
    stubDomains({ pages: [[UNKNOWN]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('newco.dev')} />)

    // The heading names the domain from the mailbox list immediately; the
    // verdict arrives with the domains query.
    expect(await screen.findByText('newco.dev')).toBeInTheDocument()
    expect(await screen.findByText('Not checked')).toBeInTheDocument()
    expect(screen.getByText(/3 mailboxes · Never checked/)).toBeInTheDocument()
    expect(screen.getByText('SPF unchecked')).toBeInTheDocument()
    expect(screen.getByText('DMARC unchecked')).toBeInTheDocument()
    expect(screen.queryByText('Action needed')).not.toBeInTheDocument()
    // "couldn't check" is not an error state on the page.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('a recheck round-trip updates the heading', async () => {
    const fixed: SendingDomain = {
      ...FAILING,
      state: 'passing',
      spf: { found: true },
      dmarc: { found: true, policy: 'reject' },
    }
    const fetchMock = stubDomains({ pages: [[FAILING], [fixed]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('startup.io')} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Recheck DNS for startup.io' }))

    await waitFor(() => expect(screen.getByText('Authenticated')).toBeInTheDocument())
    expect(screen.queryByText('Action needed')).not.toBeInTheDocument()
    const checkCalls = fetchMock.mock.calls
      .map((c) => c[0] as Request)
      .filter((req) => req.url.includes('/check'))
    expect(checkCalls.map((req) => `${req.method} ${new URL(req.url).pathname}`)).toEqual([
      'POST /api/v1/sending-domains/startup.io/check',
    ])
  })

  test('a failed recheck is surfaced on the heading and disclaims any DNS verdict', async () => {
    stubDomains({ pages: [[PASSING]], checkStatus: 502, checkBody: { error: 'resolver timeout' } })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Recheck DNS for acme.com' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Couldn't check acme\.com: resolver timeout/)
    expect(alert).toHaveTextContent(/records are unaffected/)
    // The heading keeps its previous verdict — a failed check is not a downgrade.
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
  })

  test('a domain with no verdict yet still heads its mailboxes, with no recheck to offer', async () => {
    stubDomains({ pages: [[]] })
    renderWithProviders(<Harness mailboxes={mailboxesOn('justconnected.dev')} />)

    expect(await screen.findByText('justconnected.dev')).toBeInTheDocument()
    expect(screen.getByText('3 mailboxes')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /Recheck DNS/ })).not.toBeInTheDocument(),
    )
    expect(screen.queryByText(/^SPF/)).not.toBeInTheDocument()
  })

  test('a failed load renders an error, not a silent list', async () => {
    stubDomains({ pages: { status: 500 } })
    renderWithProviders(<Harness mailboxes={mailboxesOn('acme.com')} />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load domain authentication (500)")
    expect(alert).toHaveTextContent(/says nothing about your DNS/)
  })
})
