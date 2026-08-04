import { applyStreamEvent, createAccumulator, snapshotAccumulator } from './stream-state'

describe('agent stream state', () => {
  it('coalesces adjacent text deltas but preserves reasoning order', () => {
    const accumulator = createAccumulator('run-1')
    applyStreamEvent(accumulator, { type: 'reasoning_delta', text: 'Checking ' })
    applyStreamEvent(accumulator, { type: 'reasoning_delta', text: 'mailboxes' })
    applyStreamEvent(accumulator, { type: 'text_delta', text: 'Everything ' })
    applyStreamEvent(accumulator, { type: 'text_delta', text: 'looks good.' })

    expect(snapshotAccumulator(accumulator).parts).toMatchObject([
      { type: 'reasoning', reasoning: 'Checking mailboxes' },
      { type: 'text', text: 'Everything looks good.' },
    ])
  })

  it('builds tool input across deltas and completes the matching row', () => {
    const accumulator = createAccumulator('run-2')
    applyStreamEvent(accumulator, {
      type: 'tool_input_start',
      tool_call_id: 'call-1',
      tool_name: 'inroad_mailbox_health',
    })
    applyStreamEvent(accumulator, { type: 'tool_input_delta', tool_call_id: 'call-1', text: '{"mailbox' })
    applyStreamEvent(accumulator, { type: 'tool_input_delta', tool_call_id: 'call-1', text: '_id":"mb-1"}' })
    applyStreamEvent(accumulator, {
      type: 'tool_output',
      tool_call_id: 'call-1',
      tool_output: { score: 98 },
      loading_message: 'Mailbox checked',
    })

    expect(accumulator.parts).toHaveLength(1)
    expect(accumulator.parts[0]).toMatchObject({
      tool_call_id: 'call-1',
      tool_input: { mailbox_id: 'mb-1' },
      tool_output: { score: 98 },
      loading_message: 'Mailbox checked',
      state: 'done',
    })
  })
})
