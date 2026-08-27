import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { WarmupSentinelToggle } from '../warmup-sentinel-toggle'

const jsonHeaders = { 'content-type': 'application/json' }

/** The next response the PUT gets. Steered per test; success by default. */
let respond: () => Response

/**
 * Every call made, with its body — captured inside the stub because
 * `fetchBaseQuery` passes a `Request`, whose body is a stream that can only be
 * read once and never synchronously.
 */
let calls: { url: string; method: string; body: string }[]

function sentinelWrites() {
  return calls.filter((call) => call.url.includes('/sentinel'))
}

beforeEach(() => {
  calls = []
  respond = () =>
    new Response(JSON.stringify({ mailbox_id: 'mb-1', is_sentinel: true }), { status: 200, headers: jsonHeaders })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : undefined
      const url = request?.url ?? String(input)
      calls.push({
        url,
        method: (init?.method ?? request?.method ?? 'GET').toUpperCase(),
        body: request ? await request.clone().text() : typeof init?.body === 'string' ? init.body : '',
      })
      if (url.includes('/sentinel')) return respond()
      return new Response('{}', { status: 200, headers: jsonHeaders })
    }),
  )
})

function renderToggle(isSentinel: boolean | undefined) {
  return renderWithProviders(
    <WarmupSentinelToggle mailboxId="mb-1" email="one@acme.test" isSentinel={isSentinel} />,
  )
}

/** The control as an operator finds it: by its accessible name. */
function toggle() {
  return screen.getByRole('button', { name: /sentinel for one@acme\.test/i })
}

// A build that does not report sentinels has no sentinel endpoint either, so the
// control must not exist — a button that 404s is worse than a missing feature.
test('no control at all on a build that does not report sentinels', () => {
  renderToggle(undefined)

  expect(screen.queryByRole('button')).toBeNull()
})

// The rule: designating is a real decision with a real cost, and the operator is
// told BEFORE the flip. So the first click must ask, never write.
test('the first click states the cost and sends nothing', async () => {
  renderToggle(false)

  fireEvent.click(toggle())

  await waitFor(() => expect(screen.getByRole('group', { name: /designate/i })).toBeInTheDocument())
  const prompt = document.querySelector('[data-slot="sentinel-prompt"]')?.textContent ?? ''
  expect(prompt).toMatch(/degrading/i)
  expect(prompt).toMatch(/shielded/i)
  expect(sentinelWrites()).toHaveLength(0)
})

test('confirming designates the mailbox', async () => {
  renderToggle(false)

  fireEvent.click(toggle())
  fireEvent.click(await screen.findByRole('button', { name: 'Designate as sentinel' }))

  await waitFor(() => expect(sentinelWrites()).toHaveLength(1))
  const [write] = sentinelWrites()
  expect(write?.method).toBe('PUT')
  expect(write?.url).toContain('/warmup/mailboxes/mb-1/sentinel')
  expect(JSON.parse(write?.body ?? '{}')).toEqual({ is_sentinel: true })
})

test('cancelling writes nothing and closes the prompt', async () => {
  renderToggle(false)

  fireEvent.click(toggle())
  fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))

  await waitFor(() => expect(document.querySelector('[data-slot="sentinel-prompt"]')).toBeNull())
  expect(sentinelWrites()).toHaveLength(0)
})

// Undesignating is its own decision with its own consequence — the evidence
// already gathered through it stops being corroboration.
test('undesignating asks its own question and sends false', async () => {
  renderToggle(true)

  fireEvent.click(toggle())

  const prompt = document.querySelector('[data-slot="sentinel-prompt"]')?.textContent ?? ''
  expect(prompt).toMatch(/peer-only/i)
  fireEvent.click(await screen.findByRole('button', { name: 'Stop using as sentinel' }))

  await waitFor(() => expect(sentinelWrites()).toHaveLength(1))
  expect(JSON.parse(sentinelWrites()[0]?.body ?? '{}')).toEqual({ is_sentinel: false })
})

test('a failed designation is reported rather than assumed', async () => {
  respond = () => new Response('{"error":"nope"}', { status: 500, headers: jsonHeaders })
  renderToggle(false)

  fireEvent.click(toggle())
  fireEvent.click(await screen.findByRole('button', { name: 'Designate as sentinel' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't|could not/i)
})

// 404 is the one status with a specific meaning here: the mailbox left the pool
// between the page load and the click, so retrying will not help.
test('a mailbox that has left the pool says so', async () => {
  respond = () => new Response('{"error":"nope"}', { status: 404, headers: jsonHeaders })
  renderToggle(false)

  fireEvent.click(toggle())
  fireEvent.click(await screen.findByRole('button', { name: 'Designate as sentinel' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/no longer a warmup participant/i)
})
