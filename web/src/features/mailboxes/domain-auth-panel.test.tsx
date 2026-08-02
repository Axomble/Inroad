import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { SendingDomain } from '@/store/api'
import { DomainAuthPanel } from './domain-auth-panel'
// Importing the feature api registers the sending-domain tag wiring on the
// shared endpoints registry, so a recheck invalidates the list.
import './api'

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

/**
 * Stubs `GET /sending-domains` with each response in `pages` in turn (the last
 * one repeating), and `POST /sending-domains/{domain}/check` with `checkStatus`.
 * Sequencing the list lets a recheck round-trip assert the row actually updated.
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
      return checkStatus === 200
        ? json(PASSING)
        : json(checkBody ?? { error: 'nope' }, checkStatus)
    }
    if (!Array.isArray(pages)) return json({ error: 'boom' }, pages.status)
    const page = pages[Math.min(listCall, pages.length - 1)]
    listCall += 1
    return json(page)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('DomainAuthPanel', () => {
  test('an authenticated domain shows its records, coverage, and last check', async () => {
    stubDomains({ pages: [[PASSING]] })
    renderWithProviders(<DomainAuthPanel />)

    expect(await screen.findByText('acme.com')).toBeInTheDocument()
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
    expect(screen.getByText(/3 mailboxes · Checked 1 hour ago/)).toBeInTheDocument()
    expect(screen.getByText('SPF Published')).toBeInTheDocument()
    expect(screen.getByText('DKIM Detected')).toBeInTheDocument()
    expect(screen.getByText('DMARC Enforcing (p=reject)')).toBeInTheDocument()
    // Fully passing: nothing to explain, so no notes and no alert.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('a failing domain names the DNS records to add', async () => {
    stubDomains({ pages: [[FAILING]] })
    renderWithProviders(<DomainAuthPanel />)

    expect(await screen.findByText('startup.io')).toBeInTheDocument()
    expect(screen.getByText('Action needed')).toBeInTheDocument()
    expect(
      screen.getByText(/Add an SPF TXT record at the apex and a DMARC TXT record at _dmarc\.startup\.io/),
    ).toBeInTheDocument()
    expect(screen.getByText(/starting v=spf1/)).toBeInTheDocument()
    expect(screen.getByText(/_dmarc\.startup\.io starting v=DMARC1; p=none/)).toBeInTheDocument()
  })

  test('DKIM not detected reads as advisory, not as a broken record', async () => {
    stubDomains({ pages: [[{ ...PASSING, dkim: { found: false } }]] })
    renderWithProviders(<DomainAuthPanel />)

    expect(await screen.findByText('DKIM Not detected')).toBeInTheDocument()
    expect(screen.getByText(/selectors can't be discovered from DNS/)).toBeInTheDocument()
    // The domain is still authenticated, and nothing about DKIM is a failure.
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
    expect(screen.queryByText(/DKIM.*missing/i)).not.toBeInTheDocument()
    expect(screen.getByText(/can't be discovered from DNS/)).not.toHaveClass('text-danger')
  })

  test('DMARC p=none reads as monitoring, not as protection', async () => {
    stubDomains({ pages: [[{ ...PASSING, dmarc: { found: true, policy: 'none' } }]] })
    renderWithProviders(<DomainAuthPanel />)

    expect(await screen.findByText('DMARC Monitoring only')).toBeInTheDocument()
    expect(screen.getByText(/DMARC is monitoring only — it reports, it does not enforce/)).toBeInTheDocument()
    expect(screen.getByText(/only collects reports/)).toBeInTheDocument()
    expect(screen.queryByText(/DMARC Enforcing/)).not.toBeInTheDocument()
  })

  test('an unknown domain reads as not checked, never as a failure', async () => {
    stubDomains({ pages: [[UNKNOWN]] })
    renderWithProviders(<DomainAuthPanel />)

    expect(await screen.findByText('newco.dev')).toBeInTheDocument()
    expect(screen.getByText('Not checked')).toBeInTheDocument()
    expect(screen.getByText(/3 mailboxes · Never checked/)).toBeInTheDocument()
    expect(screen.getByText('SPF Not checked')).toBeInTheDocument()
    expect(screen.getByText('DMARC Not checked')).toBeInTheDocument()
    expect(screen.queryByText('Action needed')).not.toBeInTheDocument()
    // "couldn't check" is not an error state on the page.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('a recheck round-trip updates the row', async () => {
    // Starts failing; the invalidated refetch returns the fixed domain.
    const fixed: SendingDomain = { ...FAILING, state: 'passing', spf: { found: true }, dmarc: { found: true, policy: 'reject' } }
    const fetchMock = stubDomains({ pages: [[FAILING], [fixed]] })
    renderWithProviders(<DomainAuthPanel />)

    fireEvent.click(await screen.findByRole('button', { name: 'Recheck DNS for startup.io' }))

    await waitFor(() => expect(screen.getByText('Authenticated')).toBeInTheDocument())
    expect(screen.queryByText('Action needed')).not.toBeInTheDocument()
    const checkCalls = fetchMock.mock.calls
      .map((c) => c[0] as Request)
      .filter((req) => req.url.includes('/check'))
    expect(checkCalls).toHaveLength(1)
    expect(checkCalls.map((req) => `${req.method} ${new URL(req.url).pathname}`)).toEqual([
      'POST /api/v1/sending-domains/startup.io/check',
    ])
  })

  test('a failed recheck is surfaced on the row and disclaims any DNS verdict', async () => {
    stubDomains({ pages: [[PASSING]], checkStatus: 502, checkBody: { error: 'resolver timeout' } })
    renderWithProviders(<DomainAuthPanel />)

    fireEvent.click(await screen.findByRole('button', { name: 'Recheck DNS for acme.com' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Couldn't check acme\.com: resolver timeout/)
    expect(alert).toHaveTextContent(/records are unaffected/)
    // The row keeps its previous verdict — a failed check is not a downgrade.
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
  })

  test('a failed load renders an error, not an empty panel', async () => {
    stubDomains({ pages: { status: 500 } })
    renderWithProviders(<DomainAuthPanel />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load domain authentication (500)")
    expect(alert).toHaveTextContent(/says nothing about your DNS/)
    // The section header stays so the failure is attributable to this panel.
    expect(screen.getByRole('region', { name: 'Domain authentication' })).toBeInTheDocument()
  })

  test('no sending domains renders nothing at all', async () => {
    stubDomains({ pages: [[]] })
    const { container } = renderWithProviders(<DomainAuthPanel />)

    await waitFor(() => expect(screen.queryByText('Domain authentication')).not.toBeInTheDocument())
    expect(container).toBeEmptyDOMElement()
  })
})
