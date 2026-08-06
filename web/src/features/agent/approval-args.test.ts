import { describe, expect, it } from 'vitest'
import { createDraft, diffArguments, draftArguments, hasRenderedView } from './approval-args'

describe('approval argument drafts', () => {
  it('round-trips a campaign control action without losing unknown keys', () => {
    const draft = createDraft('inroad_campaign_control', {
      method: 'pause',
      campaign_id: 'c-1',
      loading_message: 'Pausing Q3',
    })
    expect(draft).toMatchObject({ method: 'pause', campaignId: 'c-1' })

    const result = draftArguments(draft)
    expect(result).toEqual({
      ok: true,
      value: { loading_message: 'Pausing Q3', method: 'pause', campaign_id: 'c-1' },
    })
  })

  it('refuses a campaign method the tool does not accept', () => {
    const draft = createDraft('inroad_campaign_control', { method: 'delete', campaign_id: 'c-1' })
    expect(draftArguments(draft)).toEqual({
      ok: false,
      message: 'Choose whether to pause or resume the campaign.',
    })
  })

  it('reports the row number of a bad email in an import', () => {
    const draft = createDraft('inroad_contacts_import', {
      list_id: 'l-1',
      contacts: [{ email: 'ada@example.com' }, { email: 'nope' }],
    })
    expect(draftArguments(draft)).toEqual({ ok: false, message: 'Row 2 needs a valid email address.' })
  })

  it('will not submit an import emptied of every row', () => {
    const draft = createDraft('inroad_contacts_import', { list_id: 'l-1', contacts: [] })
    expect(draftArguments(draft)).toEqual({
      ok: false,
      message: 'Keep at least one contact, or reject the action instead.',
    })
  })

  it('falls back to raw JSON for a tool with no rendered view', () => {
    expect(hasRenderedView('inroad_thread_send')).toBe(false)
    const draft = createDraft('inroad_thread_send', { to: 'someone' })
    expect(draft).toEqual({ tool: 'json', text: '{\n  "to": "someone"\n}' })
    expect(draftArguments({ tool: 'json', text: 'not json' })).toEqual({
      ok: false,
      message: 'Edited arguments must be a valid JSON object.',
    })
  })

  it('diffs only the arguments that changed, summarising arrays by row count', () => {
    expect(
      diffArguments(
        { method: 'pause', campaign_id: 'c-1', contacts: [1, 2, 3] },
        { method: 'resume', campaign_id: 'c-1', contacts: [1, 2] },
      ),
    ).toEqual([
      { key: 'contacts', before: '3 rows', after: '2 rows' },
      { key: 'method', before: 'pause', after: 'resume' },
    ])
  })
})
