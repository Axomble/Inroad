import { fireEvent, screen } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { makeTestStore, renderWithProviders } from '@/test/render-with-providers'
import { setAgentPanelOpen } from '@/store/slices/ui'
import { AgentPanel } from './panel'

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  thread: vi.fn(),
  queue: vi.fn(),
  approvals: vi.fn(),
  models: vi.fn(),
  noop: vi.fn(),
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => mocks.navigate,
  // The panel selects `state.location.pathname`; hand back what that select returns.
  useRouterState: () => '/app',
  useSearch: () => ({}),
  Link: ({ to, children }: { to: string; children: React.ReactNode }) => <a href={to}>{children}</a>,
}))

// The panel owns the SSE subscription; a resize test must not open one.
vi.mock('./use-agent-stream', () => ({ useAgentStream: () => undefined }))

vi.mock('./api', async (importOriginal) => {
  const original = await importOriginal<typeof import('./api')>()
  return {
    ...original,
    useGetAgentThreadQuery: () => mocks.thread(),
    useListAgentQueueQuery: () => mocks.queue(),
    useListAgentApprovalsQuery: () => mocks.approvals(),
    useListAiModelsQuery: () => mocks.models(),
    useCreateAgentThreadMutation: () => [mocks.noop, { isLoading: false }],
    useSendAgentMessageMutation: () => [mocks.noop, { isLoading: false }],
    useStopAgentRunMutation: () => [mocks.noop, { isLoading: false }],
    useDeleteAgentQueuedMessageMutation: () => [mocks.noop, { isLoading: false }],
  }
})

// jsdom implements none of the pointer-capture API, and the repo's usual shim
// (`hasPointerCapture ??= () => false`) would make every `onPointerMove` in the
// resize handler early-return — the drag would silently do nothing and the
// tests would pass for the wrong reason. Track capture for real instead.
beforeAll(() => {
  const captured = new WeakMap<Element, Set<number>>()
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.setPointerCapture = function (this: Element, pointerId: number) {
    const ids = captured.get(this) ?? new Set<number>()
    ids.add(pointerId)
    captured.set(this, ids)
  }
  proto.releasePointerCapture = function (this: Element, pointerId: number) {
    captured.get(this)?.delete(pointerId)
  }
  proto.hasPointerCapture = function (this: Element, pointerId: number) {
    return captured.get(this)?.has(pointerId) ?? false
  }
})

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset()
  mocks.thread.mockReturnValue({ data: undefined, isLoading: false })
  mocks.queue.mockReturnValue({ data: undefined })
  mocks.approvals.mockReturnValue({ data: undefined })
  mocks.models.mockReturnValue({ data: { models: [] }, isLoading: false, isError: false })
})

/** Default width from the `ui` slice, so the arithmetic below stays readable. */
const initialWidth = 420
const minWidth = 340
const maxWidth = 640

function renderPanel() {
  const store = makeTestStore()
  store.dispatch(setAgentPanelOpen(true))
  const view = renderWithProviders(<AgentPanel />, { store })
  const handle = screen.getByRole('separator', { name: 'Resize assistant panel' })
  const panel = screen.getByRole('complementary', { name: 'Inroad assistant' })
  const renderedWidth = () => panel.style.getPropertyValue('--agent-panel-width')
  return { ...view, store, handle, panel, renderedWidth }
}

describe('AgentPanel resize handle', () => {
  it('tracks the pointer while dragging and commits the width once, on release', () => {
    const { store, handle, renderedWidth } = renderPanel()

    fireEvent.pointerDown(handle, { clientX: 1000, pointerId: 1 })
    // The panel is right-aligned, so moving left must widen it.
    fireEvent.pointerMove(handle, { clientX: 940, pointerId: 1 })
    expect(renderedWidth()).toBe('480px')

    fireEvent.pointerMove(handle, { clientX: 900, pointerId: 1 })
    expect(renderedWidth()).toBe('520px')

    // Nothing is persisted mid-drag: a drag across the panel would otherwise
    // write a redux action (and a persisted value) per pointer frame.
    expect(store.getState().ui.agentPanelWidth).toBe(initialWidth)

    fireEvent.pointerUp(handle, { clientX: 900, pointerId: 1 })
    expect(store.getState().ui.agentPanelWidth).toBe(520)
  })

  it('clamps at the maximum when dragged past it', () => {
    const { store, handle, renderedWidth } = renderPanel()

    fireEvent.pointerDown(handle, { clientX: 1000, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 100, pointerId: 1 })

    expect(renderedWidth()).toBe(`${maxWidth}px`)
    fireEvent.pointerUp(handle, { clientX: 100, pointerId: 1 })
    expect(store.getState().ui.agentPanelWidth).toBe(maxWidth)
  })

  it('clamps at the minimum when dragged past it', () => {
    const { store, handle, renderedWidth } = renderPanel()

    fireEvent.pointerDown(handle, { clientX: 1000, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 1400, pointerId: 1 })

    expect(renderedWidth()).toBe(`${minWidth}px`)
    fireEvent.pointerUp(handle, { clientX: 1400, pointerId: 1 })
    expect(store.getState().ui.agentPanelWidth).toBe(minWidth)
  })

  it('keeps the width the user last saw when the drag is cancelled', () => {
    const { store, handle, renderedWidth } = renderPanel()

    fireEvent.pointerDown(handle, { clientX: 1000, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 940, pointerId: 1 })
    expect(renderedWidth()).toBe('480px')

    // A cancelled gesture (the browser taking over, a lost touch) fires no
    // pointerup. Without handling it the inline width and the store disagree,
    // and the panel snaps back to its old width on the next render.
    fireEvent.pointerCancel(handle, { clientX: 940, pointerId: 1 })

    expect(store.getState().ui.agentPanelWidth).toBe(480)
  })

  it('ignores pointer movement that is not part of a drag', () => {
    const { store, handle, renderedWidth } = renderPanel()

    fireEvent.pointerMove(handle, { clientX: 600, pointerId: 1 })

    expect(renderedWidth()).toBe(`${initialWidth}px`)
    expect(store.getState().ui.agentPanelWidth).toBe(initialWidth)
  })

  it('resizes from the keyboard and clamps at both bounds', () => {
    const { store, handle } = renderPanel()

    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(store.getState().ui.agentPanelWidth).toBe(444)
    expect(handle).toHaveAttribute('aria-valuenow', '444')

    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(store.getState().ui.agentPanelWidth).toBe(initialWidth)

    fireEvent.keyDown(handle, { key: 'Home' })
    expect(store.getState().ui.agentPanelWidth).toBe(minWidth)
    // Home is already the minimum; a further step must not go under it.
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(store.getState().ui.agentPanelWidth).toBe(minWidth)

    fireEvent.keyDown(handle, { key: 'End' })
    expect(store.getState().ui.agentPanelWidth).toBe(maxWidth)
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(store.getState().ui.agentPanelWidth).toBe(maxWidth)
  })

  it('publishes the resize range to assistive technology', () => {
    const { handle } = renderPanel()

    expect(handle).toHaveAttribute('aria-valuemin', String(minWidth))
    expect(handle).toHaveAttribute('aria-valuemax', String(maxWidth))
    expect(handle).toHaveAttribute('aria-valuenow', String(initialWidth))
    expect(handle).toHaveAttribute('aria-valuetext', `${initialWidth} pixels wide`)
    expect(handle).toHaveAttribute('aria-orientation', 'vertical')
  })
})
