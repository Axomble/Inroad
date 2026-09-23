import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import type { Connection, Edge } from '@xyflow/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { installReactFlowDom } from '@/test/react-flow-dom'
import type { StepBranch } from '../api'
import { SequenceEditor } from '../sequence-editor'
import { bodiesTo, installFakeSequenceServer, jsonResponse, type FakeSequenceServer } from './fake-sequence-server'

// Conditions on the flow canvas, through the real React Flow + dagre pipeline
// against a stateful fake of the sequence endpoints (branch writes included).
// As in sequence-canvas.test.tsx, the handlers the canvas gives React Flow are
// captured so a drop can be driven without jsdom geometry.
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

beforeAll(async () => {
  await import('../sequence-canvas')
}, 30_000)

beforeAll(() => {
  installReactFlowDom()
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

let server: FakeSequenceServer

beforeEach(() => {
  server = installFakeSequenceServer([
    { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro', body_html: '<p>Hi</p>' },
    { id: 's-2', step_order: 2, delay_seconds: 2 * 86400, subject: 'Bump' },
    { id: 's-3', step_order: 3, delay_seconds: 86400, subject: 'Case study' },
  ])
  server.replyLabels = [
    { key: 'interested', label: 'Interested', stops_enrollment: true },
    { key: 'question', label: 'Question', stops_enrollment: false },
  ]
  canvasProps.current = undefined
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function branch(stepId: string, fields: Partial<StepBranch>): StepBranch {
  return {
    step_id: stepId,
    condition: 'opened',
    within_days: 3,
    reply_label_key: null,
    yes_step_id: null,
    no_step_id: null,
    updated_at: '2026-09-23T00:00:00Z',
    ...fields,
  }
}

async function renderCanvas(status = 'draft') {
  server.campaign.status = status
  renderWithProviders(<SequenceEditor campaignId="c-1" status={status} />)
  return screen.findByRole('region', { name: 'Sequence flow' })
}

const branchWrites = (method: string) => bodiesTo(server, '/branch', method)

async function openNewCondition(canvas: HTMLElement, position: number) {
  const trigger = within(canvas).getByRole('button', { name: `Add a condition after step ${position}` })
  trigger.focus()
  fireEvent.click(trigger)
  return screen.findByRole('complementary', { name: `Condition after step ${position}` })
}

// --- Rendering ----------------------------------------------------------

test('a linear sequence draws exactly as before: no condition nodes, one Stop', async () => {
  const canvas = await renderCanvas()
  expect(within(canvas).queryByRole('button', { name: /^Edit the condition/ })).not.toBeInTheDocument()
  expect(within(canvas).getAllByText('Stop')).toHaveLength(1)
  // Every step offers a condition.
  for (const position of [1, 2, 3]) {
    expect(within(canvas).getByRole('button', { name: `Add a condition after step ${position}` })).toBeEnabled()
  }
})

test('a branched sequence draws the IF with labelled exits and a Stop per unconnected exit', async () => {
  server.branches.set('s-1', branch('s-1', { yes_step_id: 's-3' }))
  const canvas = await renderCanvas()

  const condition = await within(canvas).findByRole('button', {
    name: 'Edit the condition after step 1: Opened within 3 days',
  })
  expect(condition).toBeInTheDocument()
  expect(within(canvas).getByText('Yes')).toBeInTheDocument()
  expect(within(canvas).getByText('No')).toBeInTheDocument()
  // No → ends here; step 3 → its own end.
  expect(within(canvas).getAllByText('Stop')).toHaveLength(2)
  // The step that already has one offers no second condition.
  expect(within(canvas).queryByRole('button', { name: 'Add a condition after step 1' })).not.toBeInTheDocument()
  // Step 2 is only reachable by falling through from step 1 — which the condition replaced.
  expect(within(canvas).getByText('Not reached')).toBeInTheDocument()
})

// --- Adding and editing ------------------------------------------------------

test('adding a condition: pick the condition, window, label and exits; it saves and takes focus', async () => {
  const canvas = await renderCanvas()
  const panel = await openNewCondition(canvas, 1)

  fireEvent.change(within(panel).getByLabelText('If they…'), { target: { value: 'replied' } })
  fireEvent.change(within(panel).getByLabelText('Within (days)'), { target: { value: '5' } })

  // Only labels that don't stop the sequence are offered, and the editor says why.
  const labelSelect = within(panel).getByLabelText('With the reply label')
  const offered = within(labelSelect).getAllByRole('option').map((option) => option.textContent)
  expect(offered).toEqual(['Any reply that doesn’t stop the sequence', 'Question'])
  expect(within(panel).getByText(/1 label stops the sequence and isn’t offered/)).toBeInTheDocument()
  fireEvent.change(labelSelect, { target: { value: 'question' } })

  fireEvent.change(within(panel).getByLabelText('Yes — go to'), { target: { value: 's-3' } })
  // No stays "End the sequence".
  fireEvent.click(within(panel).getByRole('button', { name: 'Add condition' }))

  await waitFor(() =>
    expect(branchWrites('PUT')).toEqual([
      { condition: 'replied', within_days: 5, reply_label_key: 'question', yes_step_id: 's-3', no_step_id: null },
    ]),
  )
  const node = await within(canvas).findByRole('button', {
    name: 'Edit the condition after step 1: Replied · Question within 5 days',
  })
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
  await waitFor(() => expect(node).toHaveFocus())
})

test('the editor refuses what the server would, and says why before anything is sent', async () => {
  server.campaign.tracking_enabled = false
  const canvas = await renderCanvas()
  const panel = await openNewCondition(canvas, 1)
  const save = within(panel).getByRole('button', { name: 'Add condition' })

  // "Opened" is the default; with tracking off it can never be true.
  expect(await within(panel).findByText(/Tracking is off for this campaign/)).toBeInTheDocument()
  expect(save).toBeDisabled()

  fireEvent.change(within(panel).getByLabelText('If they…'), { target: { value: 'not_replied' } })
  expect(save).toBeEnabled()

  fireEvent.change(within(panel).getByLabelText('Within (days)'), { target: { value: '91' } })
  expect(within(panel).getByText('A whole number of days from 1 to 90.')).toBeInTheDocument()
  expect(save).toBeDisabled()

  // "Always" has no window and a single exit.
  fireEvent.change(within(panel).getByLabelText('If they…'), { target: { value: 'always' } })
  expect(within(panel).queryByLabelText('Within (days)')).not.toBeInTheDocument()
  expect(within(panel).queryByLabelText('No — go to')).not.toBeInTheDocument()
  expect(within(panel).getByLabelText('Then go to')).toBeInTheDocument()
  expect(save).toBeEnabled()
  expect(branchWrites('PUT')).toEqual([])
})

test('an exit can’t name its own step', async () => {
  const canvas = await renderCanvas()
  const panel = await openNewCondition(canvas, 2)
  const options = within(within(panel).getByLabelText('Yes — go to')).getAllByRole('option')
  expect(options.map((option) => option.textContent)).toEqual([
    'End the sequence',
    'Step 1 — Intro',
    'Step 3 — Case study',
  ])
})

test('a refusal the client couldn’t foresee shows the server’s code as plain copy', async () => {
  server.branchFails = jsonResponse({ error: 'no', code: 'tracking_required' }, 400)
  const canvas = await renderCanvas()
  const panel = await openNewCondition(canvas, 1)
  fireEvent.click(within(panel).getByRole('button', { name: 'Add condition' }))
  const alert = await within(panel).findByRole('alert')
  expect(alert).toHaveTextContent(/tracking is on for this campaign.*Overview tab/)
  // The panel stays open so the user can change the condition.
  expect(screen.getByRole('complementary', { name: 'Condition after step 1' })).toBeInTheDocument()
})

test('a loop the server refuses is marked on the steps in it', async () => {
  server.branchFails = jsonResponse({ error: 'loop', code: 'cycle', step_ids: ['s-1', 's-2', 's-3'] }, 422)
  const canvas = await renderCanvas()
  const panel = await openNewCondition(canvas, 3)
  fireEvent.change(within(panel).getByLabelText('If they…'), { target: { value: 'always' } })
  fireEvent.change(within(panel).getByLabelText('Then go to'), { target: { value: 's-1' } })
  fireEvent.click(within(panel).getByRole('button', { name: 'Add condition' }))

  expect(await within(panel).findByRole('alert')).toHaveTextContent(/would make a loop/)
  await waitFor(() => expect(within(canvas).getAllByText('In a loop')).toHaveLength(3))
})

test('removing a condition returns the step to falling through, and focus to "Add a condition"', async () => {
  server.branches.set('s-1', branch('s-1', { yes_step_id: 's-3' }))
  const canvas = await renderCanvas()
  fireEvent.click(await within(canvas).findByRole('button', { name: /^Edit the condition after step 1/ }))
  const panel = await screen.findByRole('complementary', { name: 'Condition after step 1' })
  // The saved branch fills the form.
  expect(within(panel).getByLabelText('Yes — go to')).toHaveValue('s-3')
  fireEvent.click(within(panel).getByRole('button', { name: 'Remove condition' }))

  await waitFor(() => expect(bodiesTo(server, '/steps/s-1/branch', 'DELETE')).toHaveLength(1))
  const add = await within(canvas).findByRole('button', { name: 'Add a condition after step 1' })
  await waitFor(() => expect(add).toHaveFocus())
  expect(within(canvas).queryByText('Not reached')).not.toBeInTheDocument()
})

// --- Dragging an exit ------------------------------------------------------

const drag = (source: string, target: string, sourceHandle: string | null): Connection => ({
  source,
  target,
  sourceHandle,
  targetHandle: null,
})

test('dragging a condition’s No exit onto a step sets that exit and keeps the rest', async () => {
  server.branches.set('s-1', branch('s-1', { yes_step_id: 's-3' }))
  await renderCanvas()
  await screen.findByRole('button', { name: /^Edit the condition after step 1/ })
  const props = canvasProps.current
  expect(props?.isValidConnection?.(drag('cond:s-1', 's-2', 'no'))).toBe(true)
  expect(props?.isValidConnection?.(drag('cond:s-1', 's-1', 'no'))).toBe(false)

  await act(async () => props?.onConnect?.(drag('cond:s-1', 's-2', 'no')))
  await waitFor(() =>
    expect(branchWrites('PUT')).toEqual([
      { condition: 'opened', within_days: 3, reply_label_key: null, yes_step_id: 's-3', no_step_id: 's-2' },
    ]),
  )
  // The step that now owns its routing can't also be dragged as a reorder.
  expect(props?.isValidConnection?.(drag('s-1', 's-3', null))).toBe(false)
})

test('a refused drag shows why in the banner', async () => {
  server.branches.set('s-1', branch('s-1', {}))
  server.branchFails = jsonResponse({ error: 'gone', code: 'unknown_target' }, 422)
  await renderCanvas()
  await screen.findByRole('button', { name: /^Edit the condition after step 1/ })
  await act(async () => canvasProps.current?.onConnect?.(drag('cond:s-1', 's-2', 'yes')))
  expect(await screen.findByRole('alert')).toHaveTextContent(/points at a step that no longer exists/)
})

// --- Status ---------------------------------------------------------------

test('a running campaign can add and route conditions but not change structure', async () => {
  server.branches.set('s-2', branch('s-2', {}))
  const canvas = await renderCanvas('running')
  await screen.findByRole('button', { name: /^Edit the condition after step 2/ })

  // Structure is locked…
  expect(within(canvas).queryByRole('button', { name: /^Move step/ })).not.toBeInTheDocument()
  expect(within(canvas).queryByRole('button', { name: /^Add a step/ })).not.toBeInTheDocument()
  expect(canvasProps.current?.isValidConnection?.(drag('s-1', 's-3', null))).toBe(false)
  // …routing isn't: the condition's exits can be dragged, and another condition added.
  expect(canvas.querySelectorAll('.react-flow__node[data-id="cond:s-2"] .react-flow__handle.source.connectable')).toHaveLength(2)
  expect(canvasProps.current?.isValidConnection?.(drag('cond:s-2', 's-1', 'yes'))).toBe(true)

  const panel = await openNewCondition(canvas, 1)
  fireEvent.change(within(panel).getByLabelText('If they…'), { target: { value: 'not_replied' } })
  fireEvent.click(within(panel).getByRole('button', { name: 'Add condition' }))
  await waitFor(() => expect(bodiesTo(server, '/steps/s-1/branch', 'PUT')).toHaveLength(1))
})

// --- Degraded ---------------------------------------------------------------

test('if the routing can’t load, the flow falls back to the steps in order and offers no conditions', async () => {
  server.graphFails = true
  const canvas = await renderCanvas()
  expect(await screen.findByRole('alert')).toHaveTextContent(/Couldn’t load this sequence’s conditions/)
  expect(within(canvas).queryByRole('button', { name: /^Add a condition/ })).not.toBeInTheDocument()
  // The steps are all still there, linked in order.
  expect(within(canvas).getByRole('button', { name: 'Edit step 3: Case study' })).toBeInTheDocument()

  server.graphFails = false
  server.branches.set('s-1', branch('s-1', {}))
  fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
  expect(await within(canvas).findByRole('button', { name: /^Edit the condition after step 1/ })).toBeInTheDocument()
})

test('the list view says when there are conditions it can’t show', async () => {
  server.branches.set('s-1', branch('s-1', {}))
  await renderCanvas()
  await screen.findByRole('button', { name: /^Edit the condition after step 1/ })
  fireEvent.click(screen.getByRole('button', { name: 'List' }))
  expect(screen.getByRole('note')).toHaveTextContent(/This sequence has conditions/)
})
