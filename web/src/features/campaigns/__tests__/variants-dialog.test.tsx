import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { SequenceStep, StepVariant } from '../api'
import { splitShares } from '../variant-error'
import { VariantsDialog } from '../variants-dialog'
import type { StepWithId } from '../step-card'

const jsonHeaders = { 'content-type': 'application/json' }

const STEP: StepWithId = {
  id: 'step-1',
  step_order: 1,
  delay_seconds: 0,
  subject: 'quick question',
  body_text: 'Hi {{first_name}}',
  body_html: '',
  variant_weight: 1,
} as SequenceStep & { id: string }

const VARIANT_B: StepVariant = {
  id: 'var-b',
  step_id: 'step-1',
  label: 'B',
  weight: 1,
  subject: 'a different angle',
  body_text: 'Hey {{first_name}}',
  body_html: '',
}

let listResponder: () => Response
let writeResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

function methodOf(input: RequestInfo | URL, init?: RequestInit): string {
  return init?.method ?? (input instanceof Request ? input.method : 'GET')
}

/**
 * The JSON body of the last call with this method. fetchBaseQuery may hand fetch
 * either a Request or an (input, init) pair depending on version, so both are
 * read — asserting on only one shape makes the test fail for a reason that has
 * nothing to do with the component.
 */
async function lastBody(mock: ReturnType<typeof vi.fn>, method: string): Promise<unknown> {
  const call = [...mock.mock.calls].reverse().find(([i, init]) => methodOf(i, init) === method)
  if (!call) return undefined
  const [input, init] = call
  const request = init as RequestInit | undefined
  if (typeof request?.body === 'string') return JSON.parse(request.body)
  return input instanceof Request ? await input.clone().json() : undefined
}

beforeEach(() => {
  listResponder = () => new Response(JSON.stringify([VARIANT_B]), { status: 200, headers: jsonHeaders })
  writeResponder = () => new Response(JSON.stringify(VARIANT_B), { status: 200, headers: jsonHeaders })

  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (methodOf(input, init) === 'GET') return listResponder()
    return writeResponder()
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function render(step: StepWithId = STEP) {
  return renderWithProviders(
    <VariantsDialog campaignID="camp-1" step={step} position={1} onClose={vi.fn()} />,
  )
}

// Weights are relative, not percentages. An operator entering 3 and 1 needs to
// read 75% and 25% without doing the arithmetic themselves.
describe('splitShares', () => {
  test('derives whole percentages from relative weights', () => {
    expect(splitShares({ base: 3, b: 1 })).toEqual({ base: 75, b: 25 })
  })

  test('excludes retired arms from the denominator', () => {
    expect(splitShares({ base: 1, b: 0 })).toEqual({ base: 100, b: 0 })
  })

  // An empty map is the caller's cue that nothing can send at all — not a 0/0
  // division, and not a silent fallback to an even split.
  test('reports nothing eligible when every weight is zero', () => {
    expect(splitShares({ base: 0, b: 0 })).toEqual({})
  })
})

test('shows the base copy as variant A alongside each alternative', async () => {
  render()

  expect(await screen.findByText(/the step’s own copy/i)).toBeInTheDocument()
  expect(screen.getByText('quick question')).toBeInTheDocument()
  expect(screen.getByLabelText('Variant B label')).toHaveValue('B')
  // 1 and 1 is an even split, shown as a share rather than as raw weights.
  expect(screen.getAllByText('50% of sends')).toHaveLength(2)
})

// The base weight is only meaningful once there is something to split against;
// the send path ignores it for a step with no variants, and so does this.
test('a step with no variants says the base copy goes to everyone', async () => {
  listResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })
  render({ ...STEP, variant_weight: 0 })

  expect(await screen.findByText(/this copy goes to everyone/i)).toBeInTheDocument()
  expect(screen.queryByLabelText('Weight')).not.toBeInTheDocument()
})

test('warns loudly when every arm is retired', async () => {
  listResponder = () =>
    new Response(JSON.stringify([{ ...VARIANT_B, weight: 0 }]), { status: 200, headers: jsonHeaders })
  render({ ...STEP, variant_weight: 0 })

  expect(await screen.findByText(/can’t send anything/i)).toBeInTheDocument()
})

// A new arm starts from the step's own copy: an A/B test is a change to one
// thing, and a blank body would ship an empty email to half the audience.
test('adding a variant seeds it from the step’s copy', async () => {
  render()

  // The button EXISTS immediately, rendered disabled while the variant list is
  // in flight, so findByRole resolves before it is clickable and a click would
  // silently do nothing. Waiting for enabled is the difference between this
  // test passing on a fast machine and passing everywhere.
  const addButton = await screen.findByRole('button', { name: /add variant/i })
  await waitFor(() => expect(addButton).toBeEnabled())
  fireEvent.click(addButton)

  await waitFor(() => expect(fetchMock.mock.calls.some(([i, init]) => methodOf(i, init) === 'POST')).toBe(true))
  expect(await lastBody(fetchMock, 'POST')).toMatchObject({
    label: 'C',
    weight: 1,
    body_text: 'Hi {{first_name}}',
  })
})

// The API refuses to delete a variant that has sent, because it would fold that
// arm's results into the others. The dialog has to say why, not just fail.
test('a refused delete explains to use weight 0 instead', async () => {
  writeResponder = () =>
    new Response(
      JSON.stringify({ error: 'this variant has already sent; set its weight to 0 instead of deleting it' }),
      { status: 409, headers: jsonHeaders },
    )
  render()

  fireEvent.click(await screen.findByRole('button', { name: /delete variant b/i }))
  expect(await screen.findByText(/set its weight to 0/i)).toBeInTheDocument()
})

test('a load failure offers a retry rather than an empty split', async () => {
  listResponder = () => new Response(null, { status: 500 })
  render()

  expect(await screen.findByText(/couldn’t load this step’s variants/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
})
