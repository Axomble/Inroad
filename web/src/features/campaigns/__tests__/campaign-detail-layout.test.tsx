import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { RouterProvider, createMemoryHistory, createRouter } from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Provider } from 'react-redux'
import { routeTree } from '@/routeTree.gen'
import { makeTestStore } from '@/test/render-with-providers'
import type { RouterContext } from '@/routes/__root'

/**
 * The campaign detail route's two structural promises, which are the ticket's
 * acceptance criteria:
 *
 *   1. Navigating between tabs does not refetch the campaign header.
 *   2. Each tab renders its own section, and only that section.
 *
 * These need a REAL router over the REAL generated route tree — the sibling
 * tests in this directory mock `@tanstack/react-router` wholesale, which cannot
 * observe navigation at all. So this file drives an actual memory router and
 * counts requests per endpoint.
 */

const CAMPAIGN_ID = 'c1'

const campaign = {
  id: CAMPAIGN_ID,
  name: 'Q3 outbound',
  subject: 'quick question',
  status: 'running',
  tracking_enabled: true,
  stats: { queued: 4, sent: 120, failed: 2, skipped: 7 },
  metrics: { sent: 120, opened: 40, replied: 9, bounced: 1 },
}

/** Every request the app made, so a test can count per endpoint. */
let requests: string[] = []

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

beforeAll(() => {
  // Radix primitives inside the lifecycle menu touch pointer-capture APIs jsdom
  // does not implement; the same shim the sibling page tests install.
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  // The schedule board measures its grid to map pointers onto minutes.
  proto.scrollIntoView ??= () => {}
})

beforeEach(() => {
  requests = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      requests.push(`${method} ${url}`)

      if (url.includes('/auth/me')) {
        return jsonResponse({ id: 'u1', email: 'a@b.com', workspace_role: 'admin' })
      }
      // Order matters: the campaign detail URL is a prefix of every sub-resource.
      // Shapes come from `store/api.ts` — `listSteps` and `listCampaignEnrollments`
      // return BARE ARRAYS, and `listCampaigns`'s `providesTags` calls
      // `result.map`, so an object stub there throws inside RTK Query itself.
      if (url.includes('/steps')) return jsonResponse([])
      if (url.includes('/schedule')) return jsonResponse({ days: [], timezone: 'UTC' })
      if (url.includes('/senders')) return jsonResponse({ mailbox_ids: [], rotation: 'round_robin' })
      if (url.includes('/mailboxes')) return jsonResponse([])
      if (url.includes('/deliverability')) {
        return jsonResponse({
          verdict: 'ok',
          guardrails: { auto_pause_enabled: true, bounce_pause_pct: 8, complaint_pause_pct: 1.5 },
          pause_events: [],
          score: { value: 91, confidence: 'high', delivered: 2_400, components: [] },
        })
      }
      if (url.includes('/results')) return jsonResponse({ steps: [] })
      if (url.includes('/enrollments')) return jsonResponse([])
      if (url.includes(`/campaigns/${CAMPAIGN_ID}`)) return jsonResponse(campaign)
      // The campaigns LIST is an array; `providesTags` maps over it.
      return jsonResponse([])
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** Count of GETs for the campaign detail endpoint itself (not its children). */
function headerFetchCount() {
  return requests.filter((r) => /GET \S+\/campaigns\/c1(\?|$)/.test(r)).length
}

async function renderAt(path: string) {
  // `/app`'s `beforeLoad` guard reads auth off the store in ROUTER CONTEXT (see
  // main.tsx) and redirects to `/` without a token, so the same store must be
  // both injected there and provided to React — otherwise the guard throws
  // before any campaign route renders.
  const store = makeTestStore({
    auth: { status: 'authenticated', accessToken: 'test-token' } as never,
  })
  const router = createRouter({
    routeTree,
    // The context type is the PRODUCTION store, whose state carries
    // redux-persist's `_persist`; the test store deliberately omits persistence.
    // Only `beforeLoad`'s `getState().auth` and `dispatch` are actually read, so
    // the cast is narrowed to this one seam rather than widening the store type.
    context: { store: store as unknown as RouterContext['store'] },
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const utils = render(
    <Provider store={store}>
      <RouterProvider router={router as never} />
    </Provider>,
  )
  return { router, store, ...utils }
}

test('the campaign header renders once and survives tab navigation without refetching', async () => {
  await renderAt(`/app/campaigns/${CAMPAIGN_ID}`)

  // The header is the layout's, so it must appear before any tab is chosen.
  const title = await screen.findByText('Q3 outbound')
  await waitFor(() => expect(headerFetchCount()).toBe(1))

  const tabs = screen.getByRole('navigation', { name: 'Campaign sections' })
  fireEvent.click(within(tabs).getByRole('link', { name: 'Schedule' }))
  await waitFor(() => expect(requests.some((r) => r.includes('/schedule'))).toBe(true))
  fireEvent.click(within(tabs).getByRole('link', { name: 'Steps' }))
  await waitFor(() => expect(requests.some((r) => r.includes('/steps'))).toBe(true))
  fireEvent.click(within(tabs).getByRole('link', { name: 'Preferences' }))
  await waitFor(() => expect(requests.some((r) => r.includes('/senders'))).toBe(true))

  // The SAME DOM node, not merely equal text: the header belongs to the layout,
  // so navigating between sections must not tear it down and rebuild it. A
  // request count alone cannot prove this — RTK Query would serve a remounted
  // header from cache and issue no second request either way.
  expect(screen.getByText('Q3 outbound')).toBe(title)
  expect(title).toBeInTheDocument()
  // And the campaign really was fetched once across four sections.
  expect(headerFetchCount()).toBe(1)
})

test('each tab shows only its own section', async () => {
  await renderAt(`/app/campaigns/${CAMPAIGN_ID}`)
  await screen.findByText('Q3 outbound')

  const tabs = screen.getByRole('navigation', { name: 'Campaign sections' })

  // Overview is the index route: results are here, the schedule board is not.
  await waitFor(() => expect(requests.some((r) => r.includes('/results'))).toBe(true))
  expect(requests.some((r) => r.includes('/schedule'))).toBe(false)

  fireEvent.click(within(tabs).getByRole('link', { name: 'Schedule' }))
  await waitFor(() => expect(requests.some((r) => r.includes('/schedule'))).toBe(true))
})

test('a deep link into a tab renders that section directly', async () => {
  await renderAt(`/app/campaigns/${CAMPAIGN_ID}/preferences`)

  // The header still comes from the layout on a cold entry...
  expect(await screen.findByText('Q3 outbound')).toBeInTheDocument()
  // ...and the preferences section's own data is what loads, not the overview's.
  await waitFor(() => expect(requests.some((r) => r.includes('/senders'))).toBe(true))
  expect(requests.some((r) => r.includes('/results'))).toBe(false)
})

test('the active tab is marked for assistive tech and Overview does not stay active', async () => {
  await renderAt(`/app/campaigns/${CAMPAIGN_ID}`)
  await screen.findByText('Q3 outbound')

  const tabs = screen.getByRole('navigation', { name: 'Campaign sections' })
  const overview = within(tabs).getByRole('link', { name: 'Overview' })
  expect(overview).toHaveAttribute('data-status', 'active')

  fireEvent.click(within(tabs).getByRole('link', { name: 'Leads' }))

  // Overview is the parent path of every sibling; without `activeOptions.exact`
  // it would remain active on all five tabs at once.
  await waitFor(() => {
    expect(within(tabs).getByRole('link', { name: 'Leads' })).toHaveAttribute('data-status', 'active')
  })
  expect(within(tabs).getByRole('link', { name: 'Overview' })).not.toHaveAttribute('data-status', 'active')
})
