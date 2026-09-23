import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { installReactFlowDom } from '@/test/react-flow-dom'
import { SequenceEditor } from '../sequence-editor'

// The canvas is the editor's default view, rendered through the real React
// Flow + dagre pipeline against a stubbed `fetch` — the same seam the list's
// tests use — so these assert what a user sees and which requests go out.

// Warm the lazy canvas chunk (React Flow + dagre) once, so `React.lazy`
// resolves in a microtask rather than racing findBy*'s timeout on a cold
// transform. It is a one-off module load, not an assertion, so it gets a
// generous budget of its own: under a fully parallel suite the first transform
// of @xyflow alone can take several seconds.
beforeAll(async () => {
  await import('../sequence-canvas')
}, 30_000)

beforeAll(() => {
  installReactFlowDom()
  // Radix AlertDialog / Tooltip plumbing that jsdom doesn't implement.
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

type Step = { id: string; step_order: number; delay_seconds: number; subject: string; body_text?: string }
type CapturedRequest = { method: string; url: string; body: unknown }

let steps: Step[]
let requests: CapturedRequest[]
let reorderResponder: () => Response
let createdStep: Step

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } })
}

beforeEach(() => {
  requests = []
  steps = [
    { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro', body_text: 'Hello there friend' },
    { id: 's-2', step_order: 2, delay_seconds: 259200, subject: '', body_text: 'Following up now' },
    { id: 's-3', step_order: 3, delay_seconds: 86400, subject: 'Last call' },
  ]
  createdStep = { id: 's-new', step_order: 4, delay_seconds: 0, subject: 'Inserted' }
  reorderResponder = () => jsonResponse(steps)

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      const text = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      requests.push({ method, url, body: text ? (JSON.parse(text) as unknown) : undefined })

      if (url.endsWith('/steps/s-1/variants')) {
        return jsonResponse([
          { id: 'v-1', step_id: 's-1', label: 'B', weight: 50, subject: '', body_text: '', body_html: '' },
          { id: 'v-2', step_id: 's-1', label: 'C', weight: 50, subject: '', body_text: '', body_html: '' },
        ])
      }
      if (url.endsWith('/variants')) return jsonResponse([])
      if (url.endsWith('/steps/reorder')) return reorderResponder()
      if (/\/steps\/[^/]+$/.test(url) && method === 'PUT') return jsonResponse(steps[0])
      if (/\/steps\/[^/]+$/.test(url) && method === 'DELETE') return new Response(null, { status: 204 })
      if (url.endsWith('/steps') && method === 'POST') return jsonResponse(createdStep)
      return jsonResponse(steps)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function lastRequest(predicate: (r: CapturedRequest) => boolean): CapturedRequest | undefined {
  return [...requests].reverse().find(predicate)
}

async function renderCanvas(status = 'draft') {
  renderWithProviders(<SequenceEditor campaignId="c-1" status={status} />)
  return screen.findByRole('region', { name: 'Sequence flow' })
}

function submitForm(container: HTMLElement) {
  const form = container.querySelector('form')
  expect(form).not.toBeNull()
  if (form) fireEvent.submit(form)
}

test('the flow is the default view: Start, each step, and a Stop at the end', async () => {
  const canvas = await renderCanvas()

  expect(within(canvas).getByText('Start')).toBeInTheDocument()
  expect(within(canvas).getByText('Stop')).toBeInTheDocument()
  expect(within(canvas).getByRole('button', { name: 'Edit step 1: Intro' })).toBeInTheDocument()
  // A blank follow-up sends in the first step's thread, and says so in text.
  expect(within(canvas).getByRole('button', { name: 'Edit step 2: Re: Intro' })).toBeInTheDocument()
  expect(within(canvas).getByText('Same thread')).toBeInTheDocument()
  expect(within(canvas).getByRole('button', { name: 'Edit step 3: Last call' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Flow' })).toHaveAttribute('aria-pressed', 'true')
})

test('the wait before each step sits on the edge leading into it', async () => {
  const canvas = await renderCanvas()
  expect(await within(canvas).findByText('No wait')).toBeInTheDocument()
  expect(within(canvas).getByText('Wait 3 days')).toBeInTheDocument()
  expect(within(canvas).getByText('Wait 1 day')).toBeInTheDocument()
})

test('a step with variants shows how many, a step without shows nothing', async () => {
  const canvas = await renderCanvas()
  const first = within(canvas).getByRole('button', { name: 'Edit step 1: Intro' })
  expect(await within(first).findByText('2 variants')).toBeInTheDocument()
  const third = within(canvas).getByRole('button', { name: 'Edit step 3: Last call' })
  expect(within(third).queryByText(/variant/)).not.toBeInTheDocument()
})

test('clicking a step opens the existing step form beside the canvas and saves through updateStep', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Edit step 1: Intro' }))

  const panel = await screen.findByRole('complementary', { name: 'Step 1' })
  const subject = within(panel).getByLabelText('Subject')
  expect(subject).toHaveValue('Intro')
  fireEvent.change(subject, { target: { value: 'Intro v2' } })
  submitForm(panel)

  await waitFor(() =>
    expect(lastRequest((r) => r.method === 'PUT' && r.url.endsWith('/campaigns/c-1/steps/s-1'))?.body).toMatchObject({
      subject: 'Intro v2',
    }),
  )
  // A successful save closes the panel.
  await waitFor(() => expect(screen.queryByRole('complementary', { name: 'Step 1' })).not.toBeInTheDocument())
})

test('Escape closes the editor panel', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Edit step 2: Re: Intro' }))
  const panel = await screen.findByRole('complementary', { name: 'Step 2' })
  // Focus moves into the panel for keyboard users.
  expect(panel).toContainElement(document.activeElement as HTMLElement)
  fireEvent.keyDown(panel, { key: 'Escape' })
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
})

test('inserting on an edge mid-sequence creates the step, then places it after that node', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step after step 1' }))

  const panel = await screen.findByRole('complementary', { name: 'New step after step 1' })
  fireEvent.change(within(panel).getByLabelText('Subject'), { target: { value: 'Inserted' } })
  submitForm(panel)

  await waitFor(() =>
    expect(lastRequest((r) => r.method === 'POST' && r.url.endsWith('/steps/reorder'))?.body).toEqual({
      step_ids: ['s-1', 's-new', 's-2', 's-3'],
    }),
  )
  expect(lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))?.body).toMatchObject({
    subject: 'Inserted',
  })
})

test('inserting before Stop just appends — no reorder request', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step after step 3' }))
  const panel = await screen.findByRole('complementary', { name: 'New step after step 3' })
  submitForm(panel)

  await waitFor(() => expect(lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))).toBeDefined())
  await waitFor(() => expect(screen.queryByRole('complementary')).not.toBeInTheDocument())
  expect(lastRequest((r) => r.url.endsWith('/steps/reorder'))).toBeUndefined()
})

test('a failed placement says the step exists but landed at the end', async () => {
  reorderResponder = () => jsonResponse({ error: 'boom' }, 500)
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step at the start' }))
  const panel = await screen.findByRole('complementary', { name: 'New first step' })
  fireEvent.change(within(panel).getByLabelText('Subject'), { target: { value: 'Opener' } })
  submitForm(panel)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent("The step was added at the end. Couldn't reorder steps — try again.")
})

test('move down reorders through the reorder endpoint', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Move step 1 down' }))
  await waitFor(() =>
    expect(lastRequest((r) => r.url.endsWith('/steps/reorder'))?.body).toEqual({ step_ids: ['s-2', 's-1', 's-3'] }),
  )
  // The ends can't move further.
  expect(within(canvas).getByRole('button', { name: 'Move step 1 up' })).toBeDisabled()
  expect(within(canvas).getByRole('button', { name: 'Move step 3 down' })).toBeDisabled()
})

test('a 409 on reorder surfaces the draft-only copy in the banner', async () => {
  reorderResponder = () => jsonResponse({ error: 'campaign is not a draft' }, 409)
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Move step 2 up' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Reorder is only allowed while the campaign is a draft.')
})

test('delete goes through the shared confirmation and deleteStep', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Delete step 2' }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^delete step$/i }))
  await waitFor(() =>
    expect(lastRequest((r) => r.method === 'DELETE' && r.url.endsWith('/campaigns/c-1/steps/s-2'))).toBeDefined(),
  )
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

test('A/B opens the existing variants dialog for that step', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'A/B variants for step 1' }))
  expect(await screen.findByRole('alertdialog', { name: 'A/B variants — step 1' })).toBeInTheDocument()
})

test('a running campaign keeps edit and A/B but offers no structural change', async () => {
  const canvas = await renderCanvas('running')
  expect(within(canvas).getByRole('button', { name: 'Edit step 1: Intro' })).toBeEnabled()
  expect(within(canvas).getByRole('button', { name: 'A/B variants for step 1' })).toBeEnabled()
  expect(within(canvas).getByRole('button', { name: /delete step 1 \(disabled/i })).toBeDisabled()
  expect(within(canvas).queryByRole('button', { name: /^add a step/i })).not.toBeInTheDocument()
  expect(within(canvas).queryByRole('button', { name: /^move step/i })).not.toBeInTheDocument()
  // The waits still read — they describe the sequence, not an action.
  expect(await within(canvas).findByText('Wait 3 days')).toBeInTheDocument()
})

test('the list view is one click away and still renders the step cards', async () => {
  await renderCanvas()
  fireEvent.click(screen.getByRole('button', { name: 'List' }))
  expect(screen.queryByRole('region', { name: 'Sequence flow' })).not.toBeInTheDocument()
  expect(screen.getByText('3 days after previous')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'List' })).toHaveAttribute('aria-pressed', 'true')
})

test('a failed load shows the error, not an empty canvas', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ error: 'boom' }, 500)))
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  expect(await screen.findByRole('alert')).toHaveTextContent(/Couldn't load the sequence \(500\)/)
  expect(screen.queryByRole('region', { name: 'Sequence flow' })).not.toBeInTheDocument()
})
