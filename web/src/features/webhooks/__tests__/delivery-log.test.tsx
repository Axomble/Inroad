import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WebhookDelivery } from '@/store/api'
import { DeliveryLog } from '../delivery-log'

const jsonHeaders = { 'content-type': 'application/json' }

function delivery(overrides: Partial<WebhookDelivery> = {}): WebhookDelivery {
  return {
    id: 'dl-1',
    event_type: 'reply.received',
    status: 'delivered',
    attempts: 1,
    last_error: '',
    response_status: 200,
    created_at: new Date(Date.now() - 60_000).toISOString(),
    delivered_at: new Date().toISOString(),
    ...overrides,
  }
}

let requestedUrls: string[]
let responder: (url: string) => Response

function list(items: WebhookDelivery[], nextCursor: string | null = null): Response {
  return new Response(JSON.stringify({ items, next_cursor: nextCursor }), { status: 200, headers: jsonHeaders })
}

beforeEach(() => {
  requestedUrls = []
  responder = () => list([delivery()])

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      requestedUrls.push(url)
      return responder(url)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('deliveries are listed with their event, status and outcome', async () => {
  responder = () => list([delivery({ event_type: 'email.bounced', status: 'failed', last_error: 'smtp 550', response_status: null })])

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)

  expect(await screen.findByText('email.bounced')).toBeInTheDocument()
  expect(screen.getByText('Failed')).toBeInTheDocument()
  expect(screen.getByText('no response')).toBeInTheDocument()
  expect(screen.getByText('smtp 550')).toBeInTheDocument()
})

test('an endpoint with no deliveries yet says so, and suggests a test ping', async () => {
  responder = () => list([])

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)

  expect(await screen.findByText(/no deliveries yet/i)).toBeInTheDocument()
  expect(screen.getByText(/test ping/i)).toBeInTheDocument()
})

test('a failed read never renders as an empty, healthy log', async () => {
  responder = () => new Response(JSON.stringify({ message: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)

  expect(await screen.findByRole('alert')).toBeInTheDocument()
  expect(screen.queryByText(/no deliveries yet/i)).not.toBeInTheDocument()
})

test('the requested page size is fixed and the id is scoped to this endpoint', async () => {
  renderWithProviders(<DeliveryLog endpointId="ep-1" />)

  await screen.findByText('reply.received')
  expect(requestedUrls[0]).toMatch(/\/webhook-endpoints\/ep-1\/deliveries/)
  expect(requestedUrls[0]).toMatch(/limit=25/)
})

test('Next follows the cursor the server handed back', async () => {
  responder = (url) => (url.includes('cursor=cur-2') ? list([delivery({ id: 'dl-2' })]) : list([delivery()], 'cur-2'))

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findByText('reply.received')

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  await waitFor(() => expect(requestedUrls[requestedUrls.length - 1]).toMatch(/cursor=cur-2/))
})

test('Previous returns to a cursor-less first page without sending an empty cursor', async () => {
  responder = (url) => (url.includes('cursor=cur-2') ? list([delivery({ id: 'dl-2' })]) : list([delivery()], 'cur-2'))

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findByText('reply.received')
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(requestedUrls[requestedUrls.length - 1]).toMatch(/cursor=cur-2/))

  fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled())

  for (const url of requestedUrls) {
    expect(url).not.toMatch(/cursor=(&|$)/)
  }
})

// `next_cursor` is present-but-null on the last page for this contract (unlike
// dead-letters' absent cursor) — Next must read that as "no more", not omit the
// pager check and hide it only some of the time.
test('a null next_cursor on a full page still offers no Next', async () => {
  responder = () => list(Array.from({ length: 25 }, (_, i) => delivery({ id: `dl-${i}` })), null)

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findAllByText('reply.received')

  expect(screen.queryByRole('button', { name: 'Next' })).not.toBeInTheDocument()
})

test('a refused cursor returns to the first page and says so', async () => {
  responder = (url) =>
    url.includes('cursor=')
      ? new Response(JSON.stringify({ message: 'cursor not valid' }), { status: 400, headers: jsonHeaders })
      : list([delivery()], 'cur-2')

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findByText('reply.received')
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  const notice = await screen.findByRole('status')
  await waitFor(() => expect(notice).toHaveTextContent(/page link expired/i))
  expect(screen.getByText('reply.received')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
})

// The recovery is self-healing and the code knows it: the effect has already
// reset to the first page and the refetch is in flight. Announcing that through
// role="alert" would interrupt a screen reader with a failure that is gone by
// the next frame, so the one-frame error state renders as loading instead. The
// notice (role="status", polite) is what tells the operator what happened.
test('the self-healing 400 is never announced as an alert', async () => {
  responder = (url) =>
    url.includes('cursor=')
      ? new Response(JSON.stringify({ message: 'cursor not valid' }), { status: 400, headers: jsonHeaders })
      : list([delivery()], 'cur-2')

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findByText('reply.received')

  // A ONE-FRAME render is invisible to findBy*/waitFor: by the time either
  // polls, the effect has already reset the cursor and the alert is gone — so a
  // `queryByRole('alert')` assertion here would pass with the bug in place. A
  // MutationObserver sees the committed frame itself, which is the one a screen
  // reader would have interrupted the operator to read out.
  const announced: string[] = []
  const collect = (records: MutationRecord[]) => {
    for (const record of records) {
      // ATTRIBUTES matter as much as added nodes here: the error branch and the
      // empty-state copy are both a <p>, so React patches the existing element
      // and `role="alert"` arrives as an attribute change rather than as a new
      // node. Watching childList alone made this test pass with the bug in.
      if (record.type === 'attributes') {
        if (record.target instanceof Element && record.target.getAttribute('role') === 'alert') {
          announced.push(record.target.textContent ?? '')
        }
        continue
      }
      for (const node of record.addedNodes) {
        if (!(node instanceof Element)) continue
        if (node.getAttribute('role') === 'alert') announced.push(node.textContent ?? '')
        for (const nested of node.querySelectorAll('[role="alert"]')) {
          announced.push(nested.textContent ?? '')
        }
      }
    }
  }
  const observer = new MutationObserver(collect)
  observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['role'] })

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await screen.findByRole('status')
  collect(observer.takeRecords())
  observer.disconnect()

  expect(announced).toEqual([])
})

// The notice reports a ONE-TIME event ("we reset your position"). Left up while
// the operator pages on, it becomes a true statement about the previous page
// and a false one about this page — and nothing ever set `recovered` back to
// false, so it stayed for as long as the settings page was open.
test('the stale-cursor notice clears once the operator pages again', async () => {
  // Three pages, the third link expired: page 1 -> cur-2 -> cur-3 (400).
  responder = (url) => {
    if (url.includes('cursor=cur-3')) {
      return new Response(JSON.stringify({ message: 'cursor not valid' }), { status: 400, headers: jsonHeaders })
    }
    if (url.includes('cursor=cur-2')) {
      return list([delivery({ id: 'dl-2', event_type: 'email.bounced' })], 'cur-3')
    }
    return list([delivery()], 'cur-2')
  }

  renderWithProviders(<DeliveryLog endpointId="ep-1" />)
  await screen.findByText('reply.received')

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await screen.findByText('email.bounced')

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  const notice = await screen.findByRole('status')
  await waitFor(() => expect(notice).toHaveTextContent(/page link expired/i))

  // Back on page 1, and paging on is an ordinary hop. The notice describes what
  // happened on the LAST page, so it must not still be on screen for this one.
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await screen.findByText('email.bounced')
  await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
})
