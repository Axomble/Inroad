import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SequenceEditor } from './sequence-editor'

// Capture the DndContext.onDragEnd callback so the reorder test can invoke it
// directly — dnd-kit's pointer/keyboard drag relies on element rects that jsdom
// reports as zero, so we drive the reorder through the same handler the real
// context would call. useSortable/SortableContext are stubbed inert; arrayMove
// and coordinate helpers stay real.
const dnd = vi.hoisted(() => ({
  onDragEnd: undefined as ((event: { active: { id: string }; over: { id: string } | null }) => void) | undefined,
}))

vi.mock('@dnd-kit/core', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@dnd-kit/core')>()
  return {
    ...actual,
    DndContext: ({
      children,
      onDragEnd,
    }: {
      children: React.ReactNode
      onDragEnd?: (event: { active: { id: string }; over: { id: string } | null }) => void
    }) => {
      dnd.onDragEnd = onDragEnd
      return children
    },
  }
})

vi.mock('@dnd-kit/sortable', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@dnd-kit/sortable')>()
  return {
    ...actual,
    SortableContext: ({ children }: { children: React.ReactNode }) => children,
    useSortable: () => ({
      attributes: {},
      listeners: {},
      setNodeRef: () => {},
      transform: null,
      transition: undefined,
      isDragging: false,
    }),
  }
})

// Radix AlertDialog focus/pointer plumbing that jsdom doesn't implement.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

type Step = {
  id: string
  step_order: number
  delay_seconds: number
  subject: string
  body_text?: string
}

type CapturedRequest = { method: string; url: string; body: unknown }

let steps: Step[]
let requests: CapturedRequest[]
let stepsResponder: () => Response
let deleteResponder: () => Response

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

beforeEach(() => {
  requests = []
  steps = [
    { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro', body_text: 'Hello there friend' },
    { id: 's-2', step_order: 2, delay_seconds: 259200, subject: 'Bump', body_text: 'Following up now' },
  ]
  stepsResponder = () => jsonResponse(steps)
  deleteResponder = () => new Response(null, { status: 204 })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // RTK Query's fetchBaseQuery passes a `Request` object for mutations (with
      // a body) and `(urlString, init)` for plain GETs — read method/body from
      // whichever the caller used.
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      let bodyText: string | undefined
      if (isRequest) bodyText = await input.clone().text()
      else if (typeof init?.body === 'string') bodyText = init.body
      let body: unknown
      if (bodyText) {
        try {
          body = JSON.parse(bodyText)
        } catch {
          body = bodyText
        }
      }
      requests.push({ method, url, body })

      if (url.includes('/steps/reorder')) return jsonResponse(steps)
      if (url.match(/\/steps\/[^/]+$/) && method === 'PUT') return jsonResponse(steps[0])
      if (url.match(/\/steps\/[^/]+$/) && method === 'DELETE') return deleteResponder()
      if (url.endsWith('/steps') && method === 'POST') {
        return jsonResponse({ id: 's-3', step_order: 3, delay_seconds: 0, subject: 'New' })
      }
      return stepsResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  dnd.onDragEnd = undefined
})

function lastRequest(predicate: (r: CapturedRequest) => boolean): CapturedRequest | undefined {
  return [...requests].reverse().find(predicate)
}

test('renders steps in order with humanized delay labels', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)

  expect(await screen.findByText('Intro')).toBeInTheDocument()
  expect(screen.getByText('Bump')).toBeInTheDocument()
  // Step 1 is immediate; step 2 waits three days after the previous send.
  expect(screen.getByText('Immediately')).toBeInTheDocument()
  expect(screen.getByText('3 days after previous')).toBeInTheDocument()

  // Ordered: Step 1 label precedes Step 2 label in the DOM.
  const labels = screen.getAllByText(/^Step \d$/).map((el) => el.textContent)
  expect(labels).toEqual(['Step 1', 'Step 2'])
})

test('shows a loading skeleton before the steps resolve', () => {
  const { container } = renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull()
  expect(screen.queryByText('Intro')).not.toBeInTheDocument()
})

test('renders the empty state when there are no steps', async () => {
  steps = []
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  expect(await screen.findByText('No steps yet')).toBeInTheDocument()
})

test('surfaces a typed error banner when the list query fails', async () => {
  stepsResponder = () => jsonResponse({ error: 'boom' }, 500)
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Couldn't load the sequence \(500\) — try again\./i)
})

test('add step calls createStep with the converted delay and subject', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  await screen.findByText('Intro')

  fireEvent.click(screen.getByRole('button', { name: /^add step$/i }))

  const subject = await screen.findByLabelText('Subject')
  fireEvent.change(subject, { target: { value: 'Follow up' } })
  fireEvent.change(screen.getByLabelText('Delay · days'), { target: { value: '2' } })
  fireEvent.change(screen.getByLabelText('Hours'), { target: { value: '3' } })

  const form = subject.closest('form')
  expect(form).not.toBeNull()
  if (form) fireEvent.submit(form)

  await waitFor(() => {
    const req = lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))
    expect(req).toBeDefined()
  })
  const req = lastRequest((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/steps'))
  expect(req?.body).toMatchObject({ subject: 'Follow up', delay_seconds: 2 * 86400 + 3 * 3600 })
})

test('edit step calls updateStep against the step id', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  await screen.findByText('Intro')

  fireEvent.click(screen.getByRole('button', { name: /edit step 1/i }))

  const subject = await screen.findByLabelText('Subject')
  expect(subject).toHaveValue('Intro')
  fireEvent.change(subject, { target: { value: 'Intro v2' } })

  const form = subject.closest('form')
  expect(form).not.toBeNull()
  if (form) fireEvent.submit(form)

  await waitFor(() => {
    const req = lastRequest((r) => r.method === 'PUT' && r.url.endsWith('/campaigns/c-1/steps/s-1'))
    expect(req).toBeDefined()
  })
  const req = lastRequest((r) => r.method === 'PUT' && r.url.endsWith('/campaigns/c-1/steps/s-1'))
  expect(req?.body).toMatchObject({ subject: 'Intro v2' })
})

test('delete step confirms then calls deleteStep', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  // Enabled delete lives on the lazy sortable card — wait for it to mount so the
  // static fallback (delete disabled) isn't what we click.
  await screen.findByRole('button', { name: /reorder step 2/i })

  fireEvent.click(screen.getByRole('button', { name: /delete step 2/i }))

  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^delete step$/i }))

  await waitFor(() => {
    const req = lastRequest((r) => r.method === 'DELETE' && r.url.endsWith('/campaigns/c-1/steps/s-2'))
    expect(req).toBeDefined()
  })
  // Success (204) closes the confirm dialog.
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

test('a failed delete keeps the dialog open and surfaces a typed error', async () => {
  deleteResponder = () => jsonResponse({ error: 'campaign is not a draft' }, 409)
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  await screen.findByRole('button', { name: /reorder step 2/i })

  fireEvent.click(screen.getByRole('button', { name: /delete step 2/i }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^delete step$/i }))

  // The 409 surfaces the same typed copy the add/edit form uses, and the dialog
  // stays open so the user can see it and retry (regression: silent no-op).
  const alert = await within(dialog).findByRole('alert')
  expect(alert).toHaveTextContent(/only allowed while the campaign is a draft/i)
  expect(screen.getByRole('alertdialog')).toBeInTheDocument()
})

test('reorder calls reorderSteps with the new id order', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="draft" />)
  // Wait for the lazy sortable list (it mounts the DndContext that captures
  // onDragEnd); the static fallback never renders a reorder handle.
  await screen.findByRole('button', { name: /reorder step 1/i })

  // Move step 2 (s-2) above step 1 (s-1).
  expect(dnd.onDragEnd).toBeDefined()
  await act(async () => {
    dnd.onDragEnd?.({ active: { id: 's-2' }, over: { id: 's-1' } })
  })

  await waitFor(() => {
    const req = lastRequest((r) => r.method === 'POST' && r.url.endsWith('/steps/reorder'))
    expect(req).toBeDefined()
  })
  const req = lastRequest((r) => r.method === 'POST' && r.url.endsWith('/steps/reorder'))
  // Generated endpoint wraps the ids in a ReorderStepsRequest body.
  expect(req?.body).toEqual({ step_ids: ['s-2', 's-1'] })
})

test('non-draft campaign hides add/delete/reorder but keeps content edit', async () => {
  renderWithProviders(<SequenceEditor campaignId="c-1" status="running" />)
  await screen.findByText('Intro')

  // Edit stays enabled (content is live-reference).
  expect(screen.getByRole('button', { name: /edit step 1/i })).toBeEnabled()

  // Add is present but disabled with the draft-only hint.
  const add = screen.getByRole('button', { name: /add step \(disabled/i })
  expect(add).toBeDisabled()

  // Delete is disabled; reorder handles are absent entirely.
  expect(screen.getByRole('button', { name: /delete step 1 \(disabled/i })).toBeDisabled()
  expect(screen.queryByRole('button', { name: /reorder step/i })).not.toBeInTheDocument()

  // The static list renders in the eager chunk: the lazy sortable list (and its
  // DndContext) is never mounted, so no drag handler was ever captured.
  expect(dnd.onDragEnd).toBeUndefined()
})
