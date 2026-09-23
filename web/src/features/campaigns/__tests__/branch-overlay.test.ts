import { describe, expect, test } from 'vitest'
import type { StepBranch } from '../api'
import { applyBranchWrites, isSuperseded, type BranchWrite } from '../branch-overlay'

const branch = (stepId: string, yes: string | null): StepBranch => ({
  step_id: stepId,
  condition: 'opened',
  within_days: 3,
  reply_label_key: null,
  yes_step_id: yes,
  no_step_id: null,
  updated_at: '2026-09-23T00:00:00Z',
})

const server = new Map([['a', branch('a', 'old')]])
const write = (stepId: string, yes: string | null, savedAt: number): BranchWrite => ({
  stepId,
  branch: yes === null ? null : branch(stepId, yes),
  savedAt,
})

describe('applyBranchWrites', () => {
  test('a write the graph on screen predates is applied over it', () => {
    const shown = applyBranchWrites(server, [write('a', 'new', 100)], { isFetching: false, startedAt: 50 })
    expect(shown.get('a')?.yes_step_id).toBe('new')
  })

  test('while a refetch is in flight every write stays applied — the data shown is older than it', () => {
    const shown = applyBranchWrites(server, [write('a', 'new', 100)], { isFetching: true, startedAt: 500 })
    expect(shown.get('a')?.yes_step_id).toBe('new')
  })

  test('once a graph fetched after the write is on screen, the server wins', () => {
    const shown = applyBranchWrites(server, [write('a', 'new', 100)], { isFetching: false, startedAt: 101 })
    expect(shown.get('a')?.yes_step_id).toBe('old')
  })

  test('later writes to one step win, and a removal removes', () => {
    const writes = [write('a', 'first', 100), write('a', 'second', 110), write('b', 'x', 120), write('b', null, 130)]
    const shown = applyBranchWrites(server, writes, { isFetching: false, startedAt: 0 })
    expect(shown.get('a')?.yes_step_id).toBe('second')
    expect(shown.has('b')).toBe(false)
    // The server map itself is untouched.
    expect(server.get('a')?.yes_step_id).toBe('old')
  })

  test('isSuperseded needs a finished request that started after the write', () => {
    expect(isSuperseded(write('a', 'x', 100), { isFetching: false, startedAt: undefined })).toBe(false)
    expect(isSuperseded(write('a', 'x', 100), { isFetching: false, startedAt: 100 })).toBe(false)
    expect(isSuperseded(write('a', 'x', 100), { isFetching: false, startedAt: 101 })).toBe(true)
    expect(isSuperseded(write('a', 'x', 100), { isFetching: true, startedAt: 101 })).toBe(false)
  })
})
