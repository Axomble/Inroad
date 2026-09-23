import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { Connection, Edge } from '@xyflow/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { installReactFlowDom } from '@/test/react-flow-dom'
import { pressKey, replaceText, typeInto, warmRichTextEditors } from '@/test/rich-text'
import { SequenceEditor } from '../sequence-editor'

// The canvas is the editor's default view, rendered through the real React
// Flow + dagre pipeline against a stubbed `fetch` — the same seam the list's
// tests use — so these assert what a user sees and which requests go out.

// Drag-to-connect needs element geometry jsdom doesn't have, so the handlers
// the canvas hands React Flow are captured here and called directly — the same
// functions a real drop calls. The real FlowCanvas still renders.
const canvasProps = vi.hoisted(() => ({
  current: undefined as
    | { onConnect?: (connection: Connection) => void; isValidConnection?: (edge: Edge | Connection) => boolean }
    | undefined,
}))
vi.mock('@/components/shared/flow/flow-canvas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/shared/flow/flow-canvas')>()
  return {
    ...actual,
    FlowCanvas: (props: Parameters<typeof actual.FlowCanvas>[0]) => {
      canvasProps.current = props
      return <actual.FlowCanvas {...props} />
    },
  }
})

// Warm the lazy canvas chunk (React Flow + dagre) once, so `React.lazy`
// resolves in a microtask rather than racing findBy*'s timeout on a cold
// transform. It is a one-off module load, not an assertion, so it gets a
// generous budget of its own: under a fully parallel suite the first transform
// of @xyflow alone can take several seconds.
beforeAll(async () => {
  await Promise.all([import('../sequence-canvas'), warmRichTextEditors()])
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

// A small stateful server: reorder, create and delete change `steps`, and a
// GET returns whatever `steps` is now, so refetches are consistent with the
// mutations before them.
let steps: Step[]
let requests: CapturedRequest[]
let reorderFails: Response | null
/** When set, GET /steps waits on it — "the refetch hasn't come back yet". */
let listGate: Promise<void> | null

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } })
}

function renumber(list: Step[]): Step[] {
  return list.map((step, index) => ({ ...step, step_order: index + 1 }))
}

beforeEach(() => {
  requests = []
  steps = [
    { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro', body_text: 'Hello there friend' },
    { id: 's-2', step_order: 2, delay_seconds: 259200, subject: '', body_text: 'Following up now' },
    { id: 's-3', step_order: 3, delay_seconds: 86400, subject: 'Last call' },
  ]
  reorderFails = null
  listGate = null
  canvasProps.current = undefined

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      const text = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      const body = text ? (JSON.parse(text) as unknown) : undefined
      requests.push({ method, url, body })

      if (url.endsWith('/steps/s-1/variants')) {
        return jsonResponse([
          { id: 'v-1', step_id: 's-1', label: 'B', weight: 50, subject: '', body_text: '', body_html: '' },
          { id: 'v-2', step_id: 's-1', label: 'C', weight: 50, subject: '', body_text: '', body_html: '' },
        ])
      }
      if (url.endsWith('/variants')) return jsonResponse([])
      if (url.endsWith('/custom-fields')) return jsonResponse([])
      if (url.endsWith('/steps/reorder')) {
        if (reorderFails) return reorderFails
        const { step_ids } = body as { step_ids: string[] }
        steps = renumber(step_ids.flatMap((id) => steps.filter((step) => step.id === id)))
        return jsonResponse(steps)
      }
      if (/\/steps\/[^/]+$/.test(url) && method === 'PUT') return jsonResponse(steps[0])
      if (/\/steps\/[^/]+$/.test(url) && method === 'DELETE') {
        const id = url.split('/').at(-1)
        steps = renumber(steps.filter((step) => step.id !== id))
        return new Response(null, { status: 204 })
      }
      if (url.endsWith('/steps') && method === 'POST') {
        const created: Step = {
          id: 's-new',
          step_order: steps.length + 1,
          delay_seconds: 0,
          subject: (body as { subject?: string }).subject ?? '',
        }
        steps = [...steps, created]
        return jsonResponse(created)
      }
      if (listGate) await listGate
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
const reorderBodies = () => requests.filter((r) => r.url.endsWith('/steps/reorder')).map((r) => r.body)

async function renderCanvas(status = 'draft') {
  renderWithProviders(<SequenceEditor campaignId="c-1" status={status} />)
  return screen.findByRole('region', { name: 'Sequence flow' })
}

function submitForm(container: HTMLElement) {
  const form = container.querySelector('form')
  expect(form).not.toBeNull()
  if (form) fireEvent.submit(form)
}

/** jsdom doesn't focus a button on click the way a browser does; do both. */
function focusAndClick(element: HTMLElement) {
  element.focus()
  fireEvent.click(element)
}

// --- Rendering ----------------------------------------------------------

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

test('a short sequence gets a canvas sized to it, not the full cap', async () => {
  steps = [{ id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro' }]
  const canvas = await renderCanvas()
  const shortHeight = Number.parseFloat(canvas.parentElement?.style.height ?? '')
  expect(shortHeight).toBeGreaterThan(320)
  expect(shortHeight).toBeLessThan(720)
})

test('a twelve-step sequence stops growing at the cap', async () => {
  steps = renumber(
    Array.from({ length: 12 }, (_, index) => ({
      id: `s-${index + 1}`,
      step_order: 0,
      delay_seconds: 86400,
      subject: `Touch ${index + 1}`,
    })),
  )
  const canvas = await renderCanvas()
  expect(canvas.parentElement?.style.height).toBe('720px')
  // Every step is still in the DOM and reachable by Tab (the canvas pans to a
  // focused node; that part is covered in a real browser by Playwright).
  expect(within(canvas).getByRole('button', { name: 'Edit step 12: Touch 12' })).toBeInTheDocument()
})

// --- Edit -----------------------------------------------------------------

test('clicking a step opens the existing step form beside the canvas and saves through updateStep', async () => {
  const canvas = await renderCanvas()
  const trigger = within(canvas).getByRole('button', { name: 'Edit step 1: Intro' })
  focusAndClick(trigger)

  const panel = await screen.findByRole('complementary', { name: 'Step 1' })
  const subject = await within(panel).findByRole('textbox', { name: 'Subject' })
  expect(subject).toHaveTextContent('Intro')
  replaceText(subject, 'Intro v2')
  submitForm(panel)

  await waitFor(() =>
    expect(lastRequest((r) => r.method === 'PUT' && r.url.endsWith('/campaigns/c-1/steps/s-1'))?.body).toMatchObject({
      subject: 'Intro v2',
    }),
  )
  // A successful save closes the panel and hands focus back to the step.
  await waitFor(() => expect(screen.queryByRole('complementary', { name: 'Step 1' })).not.toBeInTheDocument())
  expect(trigger).toHaveFocus()
})

test('Escape closes the editor panel and returns focus to the step that opened it', async () => {
  const canvas = await renderCanvas()
  const trigger = within(canvas).getByRole('button', { name: 'Edit step 2: Re: Intro' })
  focusAndClick(trigger)
  const panel = await screen.findByRole('complementary', { name: 'Step 2' })
  // Focus moves into the panel for keyboard users.
  expect(panel).toContainElement(document.activeElement as HTMLElement)
  fireEvent.keyDown(panel, { key: 'Escape' })
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
  expect(trigger).toHaveFocus()
})

test('Escape that closes the merge-field menu leaves the panel and its edits open', async () => {
  const canvas = await renderCanvas()
  focusAndClick(within(canvas).getByRole('button', { name: 'Edit step 1: Intro' }))
  const panel = await screen.findByRole('complementary', { name: 'Step 1' })
  const body = await within(panel).findByRole('textbox', { name: 'Body' })

  typeInto(body, '{{')
  await within(panel).findByRole('listbox', { name: 'Merge fields' })
  pressKey(body, 'Escape')

  expect(within(panel).queryByRole('listbox')).not.toBeInTheDocument()
  expect(screen.getByRole('complementary', { name: 'Step 1' })).toBeInTheDocument()
})

// --- Add ------------------------------------------------------------------

test('inserting on an edge mid-sequence creates the step, places it, and focuses it', async () => {
  const canvas = await renderCanvas()
  focusAndClick(await within(canvas).findByRole('button', { name: 'Add a step after step 1' }))

  const panel = await screen.findByRole('complementary', { name: 'New step after step 1' })
  replaceText(await within(panel).findByRole('textbox', { name: 'Subject' }), 'Inserted')
  submitForm(panel)

  await waitFor(() => expect(reorderBodies().at(-1)).toEqual({ step_ids: ['s-1', 's-new', 's-2', 's-3'] }))
  expect(lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))?.body).toMatchObject({
    subject: 'Inserted',
  })
  // The "+" that opened the panel is gone (its edge was split), so focus goes
  // to the step that now exists in its place.
  await waitFor(() => expect(within(canvas).getByRole('button', { name: 'Edit step 2: Inserted' })).toHaveFocus())
})

test('inserting before Stop just appends — no reorder request', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step after step 3' }))
  const panel = await screen.findByRole('complementary', { name: 'New step after step 3' })
  submitForm(panel)

  await waitFor(() => expect(lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))).toBeDefined())
  await waitFor(() => expect(screen.queryByRole('complementary')).not.toBeInTheDocument())
  expect(reorderBodies()).toEqual([])
})

test('a double click on save creates the step once', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step after step 3' }))
  const panel = await screen.findByRole('complementary', { name: 'New step after step 3' })
  const save = within(panel).getByRole('button', { name: 'Add step' })
  fireEvent.click(save)
  fireEvent.click(save)

  await waitFor(() => expect(screen.queryByRole('complementary')).not.toBeInTheDocument())
  expect(requests.filter((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))).toHaveLength(1)
})

test('a failed placement says the step exists but landed at the end', async () => {
  reorderFails = jsonResponse({ error: 'boom' }, 500)
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step at the start' }))
  const panel = await screen.findByRole('complementary', { name: 'New first step' })
  replaceText(await within(panel).findByRole('textbox', { name: 'Subject' }), 'Opener')
  submitForm(panel)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent("The step was added at the end. Couldn't reorder steps — try again.")
})

test('if the step it was meant to follow is deleted meanwhile, the new step stays at the end and says so', async () => {
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: 'Add a step after step 3' }))
  await screen.findByRole('complementary', { name: 'New step after step 3' })

  // Delete the anchor while the add panel is still open.
  fireEvent.click(within(canvas).getByRole('button', { name: 'Delete step 3' }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^delete step$/i }))
  await waitFor(() => expect(within(canvas).queryByRole('button', { name: /Last call/ })).not.toBeInTheDocument())

  const panel = screen.getByRole('complementary', { name: 'New step' })
  submitForm(panel)

  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The step was added at the end. The step it was meant to follow was removed.',
  )
  expect(reorderBodies()).toEqual([])
})

test('in the flow view the section bar’s Add step opens the canvas panel after the last step — one form, not two', async () => {
  await renderCanvas()
  fireEvent.click(screen.getByRole('button', { name: /^add step$/i }))
  const panel = await screen.findByRole('complementary', { name: 'New step after step 3' })
  expect(panel.querySelector('form')).not.toBeNull()
  expect(document.querySelectorAll('form')).toHaveLength(1)
})

// --- Move -----------------------------------------------------------------

test('move down reorders through the reorder endpoint, and focus follows the moved step', async () => {
  const canvas = await renderCanvas()
  focusAndClick(within(canvas).getByRole('button', { name: 'Move step 2 down' }))
  await waitFor(() => expect(reorderBodies().at(-1)).toEqual({ step_ids: ['s-1', 's-3', 's-2'] }))
  // The step is now last, so its down arrow is disabled; focus lands on up.
  await waitFor(() => expect(within(canvas).getByRole('button', { name: 'Move step 3 up' })).toHaveFocus())
  expect(within(canvas).getByRole('button', { name: 'Move step 3 down' })).toBeDisabled()
  expect(within(canvas).getByRole('button', { name: 'Move step 1 up' })).toBeDisabled()
})

test('a second move before the refetch lands builds on the first, not on the stale order', async () => {
  const canvas = await renderCanvas()
  // From here on, the refetch the reorder's invalidation triggers never
  // returns — only the reorder response itself can update the canvas.
  let release = () => {}
  listGate = new Promise<void>((resolve) => {
    release = resolve
  })

  fireEvent.click(within(canvas).getByRole('button', { name: 'Move step 3 up' }))
  await waitFor(() => expect(reorderBodies()).toHaveLength(1))
  expect(reorderBodies()[0]).toEqual({ step_ids: ['s-1', 's-3', 's-2'] })

  // "Last call" is now step 2; move it up again.
  const next = await within(canvas).findByRole('button', { name: 'Move step 2 up' })
  await waitFor(() => expect(next).toBeEnabled())
  fireEvent.click(next)
  await waitFor(() => expect(reorderBodies()).toHaveLength(2))
  expect(reorderBodies()[1]).toEqual({ step_ids: ['s-3', 's-1', 's-2'] })

  await act(async () => release())
})

test('a blank-subject follow-up can’t be moved into first place', async () => {
  const canvas = await renderCanvas()
  // Step 2 has no subject: neither moving it up nor moving step 1 below it is offered.
  const up = within(canvas).getByRole('button', { name: /^Move step 2 up \(disabled — The first step opens the thread/ })
  expect(up).toBeDisabled()
  expect(within(canvas).getByRole('button', { name: /^Move step 1 down \(disabled/ })).toBeDisabled()
  // A move that keeps a subject first is still fine.
  expect(within(canvas).getByRole('button', { name: 'Move step 3 up' })).toBeEnabled()
})

test('a 409 on reorder surfaces the draft-only copy in the banner', async () => {
  reorderFails = jsonResponse({ error: 'campaign is not a draft' }, 409)
  const canvas = await renderCanvas()
  fireEvent.click(within(canvas).getByRole('button', { name: 'Move step 3 up' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Reorder is only allowed while the campaign is a draft.')
})

// --- Connect --------------------------------------------------------------

const connection = (source: string, target: string): Connection => ({
  source,
  target,
  sourceHandle: null,
  targetHandle: null,
})

test('dropping a connection from one step onto another makes it next', async () => {
  await renderCanvas()
  const props = canvasProps.current
  expect(props?.onConnect).toBeDefined()
  expect(props?.isValidConnection?.(connection('s-1', 's-3'))).toBe(true)

  await act(async () => props?.onConnect?.(connection('s-1', 's-3')))
  await waitFor(() => expect(reorderBodies().at(-1)).toEqual({ step_ids: ['s-1', 's-3', 's-2'] }))
})

test('connections that mean nothing, or would open on a blank subject, are refused', async () => {
  await renderCanvas()
  const props = canvasProps.current
  const valid = (source: string, target: string) => props?.isValidConnection?.(connection(source, target))
  expect(valid('s-1', 's-1')).toBe(false)
  expect(valid('s-1', 'stop:s-3:out')).toBe(false)
  // Start → the blank follow-up would make it step 1.
  expect(valid('start', 's-2')).toBe(false)
  expect(valid('start', 's-3')).toBe(true)

  await act(async () => props?.onConnect?.(connection('start', 's-2')))
  expect(reorderBodies()).toEqual([])
})

test('handles are connectable on a draft and not on a running campaign', async () => {
  const draft = await renderCanvas('draft')
  expect(draft.querySelectorAll('.react-flow__handle.connectable').length).toBeGreaterThan(0)
})

test('a running campaign keeps edit and A/B but offers no structural change', async () => {
  const canvas = await renderCanvas('running')
  expect(within(canvas).getByRole('button', { name: 'Edit step 1: Intro' })).toBeEnabled()
  expect(within(canvas).getByRole('button', { name: 'A/B variants for step 1' })).toBeEnabled()
  expect(within(canvas).getByRole('button', { name: /delete step 1 \(disabled/i })).toBeDisabled()
  expect(within(canvas).queryByRole('button', { name: /^add a step/i })).not.toBeInTheDocument()
  expect(within(canvas).queryByRole('button', { name: /^move step/i })).not.toBeInTheDocument()
  // Nothing to drag: no handler, and no handle React Flow treats as connectable.
  expect(canvasProps.current?.onConnect).toBeUndefined()
  expect(canvas.querySelectorAll('.react-flow__handle.connectable')).toHaveLength(0)
  // The waits still read — they describe the sequence, not an action.
  expect(await within(canvas).findByText('Wait 3 days')).toBeInTheDocument()
})

// --- Delete, variants, view, errors --------------------------------------

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
