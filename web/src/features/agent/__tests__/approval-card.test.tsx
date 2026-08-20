import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentApproval } from '../api'
import { ApprovalCard } from '../approval-card'

const mocks = vi.hoisted(() => ({ decide: vi.fn() }))

vi.mock('../api', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api')>()
  return {
    ...original,
    useDecideAgentApprovalMutation: () => [mocks.decide, { isLoading: false }],
  }
})

const action: AgentApproval = {
  id: '11111111-1111-1111-1111-111111111111',
  workspace_id: '22222222-2222-2222-2222-222222222222',
  thread_id: '33333333-3333-3333-3333-333333333333',
  run_id: '44444444-4444-4444-4444-444444444444',
  tool_name: 'inroad_campaign_control',
  tool_call_id: 'call-1',
  arguments: { method: 'pause', campaign_id: 'campaign-1', loading_message: 'Pausing Q3' },
  risk_tier: 'consequential',
  status: 'pending',
  expires_at: '2099-08-05T12:00:00Z',
  created_at: '2026-08-05T10:00:00Z',
  updated_at: '2026-08-05T10:00:00Z',
}

const importAction: AgentApproval = {
  ...action,
  id: '55555555-5555-5555-5555-555555555555',
  tool_name: 'inroad_contacts_import',
  tool_call_id: 'call-2',
  arguments: {
    list_id: 'list-9',
    contacts: [
      { email: 'ada@example.com', first_name: 'Ada', company: 'Analytical' },
      { email: 'grace@example.com', first_name: 'Grace' },
    ],
  },
}

function rejectWith(error: unknown) {
  mocks.decide.mockReturnValue({
    unwrap: async () => {
      throw error
    },
  })
}

describe('ApprovalCard', () => {
  beforeEach(() => {
    mocks.decide.mockReset()
    mocks.decide.mockReturnValue({ unwrap: () => Promise.resolve(action) })
  })

  it('describes the effect rather than the JSON, and approves it', async () => {
    render(<ApprovalCard action={action} />)
    expect(screen.getByText('Approve Campaign Control')).toBeInTheDocument()
    expect(screen.getByText(/Pause this campaign/)).toBeInTheDocument()
    expect(screen.getByText('Pause sending')).toBeInTheDocument()
    expect(screen.getByText('campaign-1')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))
    await waitFor(() => expect(mocks.decide).toHaveBeenCalledWith({
      actionId: action.id,
      agentApprovalDecisionRequest: { decision: 'approve' },
    }))
  })

  it('edits named fields, shows a before/after diff, and preserves untouched keys', async () => {
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit inputs' }))

    expect(screen.getByText(/No changes yet/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'resume' } })

    expect(screen.getByText('1 change from what the assistant proposed')).toBeInTheDocument()
    expect(screen.getByText('method')).toBeInTheDocument()
    expect(screen.getByText('pause')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Approve edited action' }))
    await waitFor(() => expect(mocks.decide).toHaveBeenCalledWith({
      actionId: action.id,
      agentApprovalDecisionRequest: {
        decision: 'approve',
        edited_arguments: {
          loading_message: 'Pausing Q3',
          method: 'resume',
          campaign_id: 'campaign-1',
        },
      },
    }))
  })

  it('renders a bulk import as rows and diffs a removed row', () => {
    render(<ApprovalCard action={importAction} />)
    expect(screen.getByText('Import 2 contacts into one existing contact list.')).toBeInTheDocument()
    expect(screen.getByText('ada@example.com')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Edit inputs' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove row 1' }))

    expect(screen.getByText('1 change from what the assistant proposed')).toBeInTheDocument()
    expect(screen.getByText('2 rows')).toBeInTheDocument()
    expect(screen.getByText('1 row')).toBeInTheDocument()
  })

  it('rejects an invalid email in an edited import without submitting', () => {
    render(<ApprovalCard action={importAction} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit inputs' }))
    fireEvent.change(screen.getByLabelText('Email for row 2'), { target: { value: 'not-an-email' } })
    fireEvent.click(screen.getByRole('button', { name: 'Approve edited action' }))

    expect(screen.getByRole('alert')).toHaveTextContent('Row 2 needs a valid email address.')
    expect(mocks.decide).not.toHaveBeenCalled()
  })

  it('blocks malformed JSON in the raw escape hatch', () => {
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit inputs' }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit as JSON' }))
    fireEvent.change(screen.getByLabelText('Edited action inputs (JSON)'), {
      target: { value: '["not", "an", "object"]' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Approve edited action' }))

    expect(screen.getByRole('alert')).toHaveTextContent('must be a JSON object')
    expect(mocks.decide).not.toHaveBeenCalled()
  })

  it('requires a rejection reason', () => {
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    fireEvent.click(screen.getByRole('button', { name: 'Reject action' }))

    expect(screen.getByRole('alert')).toHaveTextContent('why this action should not run')
    expect(mocks.decide).not.toHaveBeenCalled()
  })

  it('tells the reviewer someone else already decided the action on a 409', async () => {
    rejectWith({ status: 409, data: { error: 'already_decided' } })
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        'This action was already decided — it expired or someone else approved or rejected it.',
      ),
    )
  })

  it('separates rejected edits (400) from a vanished action (404)', async () => {
    rejectWith({ status: 400, data: {} })
    const { unmount } = render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('The edited inputs were rejected'))
    unmount()

    rejectWith({ status: 404, data: {} })
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('This action no longer exists'))
  })

  it('reports a transport failure as a connection problem, not a stale action', async () => {
    rejectWith({ status: 'FETCH_ERROR', error: 'TypeError: failed' })
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not reach the server'))
  })

  it('counts down in seconds inside the last two minutes', () => {
    render(<ApprovalCard action={{ ...action, expires_at: new Date(Date.now() + 45_000).toISOString() }} />)
    expect(screen.getByText(/4[0-9]s remaining/)).toBeInTheDocument()
  })

  it('disables the decision buttons once a pending action has expired', () => {
    render(<ApprovalCard action={{ ...action, expires_at: new Date(Date.now() - 1000).toISOString() }} />)

    expect(screen.queryByRole('button', { name: 'Approve action' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
    expect(screen.getByText(/expired before it was reviewed/)).toBeInTheDocument()
    expect(screen.getByText('expired')).toBeInTheDocument()
  })
})
