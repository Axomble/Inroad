import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { AgentComposer } from './composer'

const mocks = vi.hoisted(() => ({
  models: vi.fn(),
  create: vi.fn(),
  send: vi.fn(),
  stop: vi.fn(),
  removeQueued: vi.fn(),
}))

// The composer only needs a router for the AI-settings link.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: React.ReactNode }) => <a href={to}>{children}</a>,
}))

vi.mock('./api', async (importOriginal) => {
  const original = await importOriginal<typeof import('./api')>()
  return {
    ...original,
    useListAiModelsQuery: () => mocks.models(),
    useCreateAgentThreadMutation: () => [mocks.create, { isLoading: false }],
    useSendAgentMessageMutation: () => [mocks.send, { isLoading: false }],
    useStopAgentRunMutation: () => [mocks.stop, { isLoading: false }],
    useDeleteAgentQueuedMessageMutation: () => [mocks.removeQueued, { isLoading: false }],
  }
})

const THREAD_ID = 'thread-1'

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset()
  mocks.models.mockReturnValue({
    data: { models: [{ id: 'claude', label: 'Claude', enabled: true }] },
    isLoading: false,
    isError: false,
  })
  mocks.create.mockReturnValue({ unwrap: () => Promise.resolve({ id: THREAD_ID }) })
  mocks.send.mockReturnValue({ unwrap: () => Promise.resolve({ run_id: 'run-1' }) })
  mocks.stop.mockReturnValue({ unwrap: () => Promise.resolve({}) })
})

function renderComposer(activeRunId: string | null = null) {
  return renderWithProviders(
    <AgentComposer threadId={THREAD_ID} activeRunId={activeRunId} onSelectThread={() => {}} />,
  )
}

describe('AgentComposer', () => {
  it('points at AI settings when no provider is configured', () => {
    mocks.models.mockReturnValue({ data: { models: [] }, isLoading: false, isError: false })
    renderComposer()

    expect(screen.getByText(/No AI provider is configured yet/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Add a provider in AI settings/ })).toHaveAttribute(
      'href',
      '/app/settings/ai',
    )
  })

  it('keeps a queueing Send button next to Stop while a run is in flight', async () => {
    renderComposer('run-1')
    expect(screen.getByRole('button', { name: 'Stop agent' })).toBeInTheDocument()

    const queue = screen.getByRole('button', { name: 'Queue message' })
    fireEvent.change(screen.getByPlaceholderText('Queue a follow-up...'), {
      target: { value: 'and check mailboxes' },
    })
    fireEvent.click(queue)

    await waitFor(() => expect(mocks.send).toHaveBeenCalled())
    expect(screen.getByText(/Enter queues this message/)).toBeInTheDocument()
  })

  it('surfaces a failed stop instead of swallowing it', async () => {
    mocks.stop.mockReturnValue({ unwrap: () => Promise.reject({ status: 500, data: {} }) })
    renderComposer('run-1')

    fireEvent.click(screen.getByRole('button', { name: 'Stop agent' }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('The server had a problem. Try again in a moment.'),
    )
  })

  it('surfaces a failed send and keeps the draft', async () => {
    mocks.send.mockReturnValue({ unwrap: () => Promise.reject({ status: 'FETCH_ERROR', error: 'x' }) })
    renderComposer()

    const textarea = screen.getByPlaceholderText('Ask Inroad anything...')
    fireEvent.change(textarea, { target: { value: 'summarise my campaigns' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not reach the server'))
    expect(textarea).toHaveValue('summarise my campaigns')
  })
})
