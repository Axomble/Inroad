import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentHistory } from './history'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  rename: vi.fn(),
  remove: vi.fn(),
}))

vi.mock('./api', async (importOriginal) => {
  const original = await importOriginal<typeof import('./api')>()
  return {
    ...original,
    useListAgentThreadsQuery: () => mocks.list(),
    useRenameAgentThreadMutation: () => [mocks.rename, { isLoading: false }],
    useDeleteAgentThreadMutation: () => [mocks.remove, { isLoading: false }],
  }
})

const thread = {
  id: 'thread-1',
  title: 'Campaign check',
  updated_at: new Date().toISOString(),
  created_at: new Date().toISOString(),
}

function listResult(overrides: Record<string, unknown> = {}) {
  return {
    data: { threads: [thread] },
    isLoading: false,
    isError: false,
    error: undefined,
    refetch: vi.fn(),
    ...overrides,
  }
}

beforeEach(() => {
  mocks.list.mockReset()
  mocks.rename.mockReset()
  mocks.remove.mockReset()
  mocks.list.mockReturnValue(listResult())
  mocks.rename.mockReturnValue({ unwrap: () => Promise.resolve(thread) })
  mocks.remove.mockReturnValue({ unwrap: () => Promise.resolve({}) })
})

function renderHistory() {
  return render(<AgentHistory activeThreadId="thread-1" onSelect={() => {}} onNew={() => {}} />)
}

describe('AgentHistory', () => {
  it('tells the user when a rename failed instead of letting the title revert silently', async () => {
    mocks.rename.mockReturnValue({ unwrap: () => Promise.reject({ status: 500, data: {} }) })
    renderHistory()

    fireEvent.click(screen.getByRole('button', { name: 'Rename thread' }))
    fireEvent.change(screen.getByDisplayValue('Campaign check'), { target: { value: 'Q3 review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save title' }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('The server had a problem. Try again in a moment.'),
    )
  })

  it('cancels a rename back to the original title', () => {
    renderHistory()
    fireEvent.click(screen.getByRole('button', { name: 'Rename thread' }))
    fireEvent.change(screen.getByDisplayValue('Campaign check'), { target: { value: 'scratch' } })
    fireEvent.keyDown(screen.getByDisplayValue('scratch'), { key: 'Escape' })

    expect(mocks.rename).not.toHaveBeenCalled()
    expect(screen.getByText('Campaign check')).toBeInTheDocument()
  })

  it('reports a failed archive rather than dropping the rejection', async () => {
    mocks.remove.mockReturnValue({ unwrap: () => Promise.reject({ status: 403, data: {} }) })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderHistory()

    fireEvent.click(screen.getByRole('button', { name: 'Archive thread' }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('You do not have permission to do that.'),
    )
  })

  it('distinguishes a failed list from an empty one', () => {
    mocks.list.mockReturnValue(
      listResult({ data: undefined, isError: true, error: { status: 500, data: {} } }),
    )
    renderHistory()

    expect(screen.getByText('The server had a problem. Try again in a moment.')).toBeInTheDocument()
    expect(screen.queryByText('No conversations yet.')).not.toBeInTheDocument()
  })
})
