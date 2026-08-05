import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentApproval } from './api'
import { ApprovalCard } from './approval-card'

const mocks = vi.hoisted(() => ({ decide: vi.fn() }))

vi.mock('./api', async (importOriginal) => {
  const original = await importOriginal<typeof import('./api')>()
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
  arguments: { method: 'pause', campaign_id: 'campaign-1' },
  risk_tier: 'consequential',
  status: 'pending',
  expires_at: '2099-08-05T12:00:00Z',
  created_at: '2026-08-05T10:00:00Z',
  updated_at: '2026-08-05T10:00:00Z',
}

describe('ApprovalCard', () => {
  beforeEach(() => {
    mocks.decide.mockReset()
    mocks.decide.mockReturnValue({ unwrap: () => Promise.resolve(action) })
  })

  it('previews the exact action and approves it', async () => {
    render(<ApprovalCard action={action} />)
    expect(screen.getByText('Approve Campaign Control')).toBeInTheDocument()
    expect(screen.getByText(/campaign_id/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Approve action' }))
    await waitFor(() => expect(mocks.decide).toHaveBeenCalledWith({
      actionId: action.id,
      agentApprovalDecisionRequest: { decision: 'approve' },
    }))
  })

  it('blocks malformed edited arguments without submitting', () => {
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit inputs' }))
    fireEvent.change(screen.getByLabelText('Edited action inputs (JSON)'), { target: { value: '["not", "an", "object"]' } })
    fireEvent.click(screen.getByRole('button', { name: 'Approve edited action' }))

    expect(screen.getByRole('alert')).toHaveTextContent('valid JSON object')
    expect(mocks.decide).not.toHaveBeenCalled()
  })

  it('requires a rejection reason', () => {
    render(<ApprovalCard action={action} />)
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    fireEvent.click(screen.getByRole('button', { name: 'Reject action' }))

    expect(screen.getByRole('alert')).toHaveTextContent('why this action should not run')
    expect(mocks.decide).not.toHaveBeenCalled()
  })
})
