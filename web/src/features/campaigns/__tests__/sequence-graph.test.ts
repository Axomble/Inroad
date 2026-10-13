import { describe, expect, test } from 'vitest'
import {
  START_NODE_ID,
  branchesByStep,
  buildSequenceGraph,
  exitForConnection,
  isMeaningfulConnection,
  leadsWithSubject,
  moveStep,
  orderChanged,
  orderForConnection,
  placeAfter,
} from '../sequence-graph'
import { sequenceNodeRegistry } from '../sequence-node-registry'
import type { StepBranch } from '../api'
import type { StepWithId } from '../step-card'

const steps: StepWithId[] = [
  { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro' },
  { id: 's-2', step_order: 2, delay_seconds: 3 * 86400, subject: '' },
]
const outputsOf = sequenceNodeRegistry.outputsOf

/** The linear defaults; each test overrides what it's about. */
function options(overrides: Partial<Parameters<typeof buildSequenceGraph>[1]> = {}) {
  return {
    canModifyStructure: true,
    canEditBranches: true,
    branches: new Map<string, StepBranch>(),
    loopStepIds: new Set<string>(),
    outputsOf,
    ...overrides,
  }
}

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

describe('buildSequenceGraph', () => {
  test('Start → each step in order → one Stop after the last', () => {
    const { nodes, edges } = buildSequenceGraph(steps, options())
    expect(nodes.map((n) => `${n.type}:${n.id}`)).toEqual([
      'start:start',
      'step:s-1',
      'step:s-2',
      'stop:stop:s-2:out',
    ])
    expect(edges.map((e) => `${e.source}->${e.target}`)).toEqual([
      'start->s-1',
      's-1->s-2',
      's-2->stop:s-2:out',
    ])
  })

  test('the edge into a step carries its wait; the edge into Stop carries none', () => {
    const { edges } = buildSequenceGraph(steps, options())
    expect(edges.map((e) => e.data?.label)).toEqual(['No wait', 'Wait 3 days', undefined])
  })

  test('every edge offers an insert while the structure is editable, named by position', () => {
    const editable = buildSequenceGraph(steps, options())
    expect(editable.edges.map((e) => e.data?.insertLabel)).toEqual([
      'Add a step at the start',
      'Add a step after step 1',
      'Add a step after step 2',
    ])
    const locked = buildSequenceGraph(steps, options({ canModifyStructure: false }))
    expect(locked.edges.every((e) => e.data?.insertLabel === undefined)).toBe(true)
  })

  test('a step node knows its position and the thread subject for blank follow-ups', () => {
    const { nodes } = buildSequenceGraph(steps, options({ canModifyStructure: false }))
    const second = nodes.find((n) => n.id === 's-2')
    expect(second?.type === 'step' && second.data).toMatchObject({
      position: 2,
      threadSubject: 'Intro',
      canModifyStructure: false,
    })
  })

  test('with no steps, Start runs straight to Stop', () => {
    const { nodes, edges } = buildSequenceGraph([], options())
    expect(nodes.map((n) => n.type)).toEqual(['start', 'stop'])
    expect(edges).toHaveLength(1)
    expect(edges[0]?.data?.insertLabel).toBe('Add a step at the start')
  })
})

describe('buildSequenceGraph with conditions', () => {
  const four: StepWithId[] = [
    { id: 'a', step_order: 1, delay_seconds: 0, subject: 'Intro' },
    { id: 'b', step_order: 2, delay_seconds: 86400, subject: 'Bump' },
    { id: 'c', step_order: 3, delay_seconds: 2 * 86400, subject: 'Case study' },
    { id: 'd', step_order: 4, delay_seconds: 0, subject: 'Breakup' },
  ]
  const summary = (graph: ReturnType<typeof buildSequenceGraph>) =>
    graph.edges.map((e) => `${e.source}${e.sourceHandle ? `:${e.sourceHandle}` : ''}->${e.target}`)

  test('a step with a condition routes through its IF node; each exit goes where the branch says', () => {
    const graph = buildSequenceGraph(
      four,
      options({ branches: new Map([['a', branch('a', { yes_step_id: 'c', no_step_id: 'b' })]]) }),
    )
    // The condition sits right after its step.
    expect(graph.nodes.map((n) => n.id).slice(0, 4)).toEqual(['start', 'a', 'cond:a', 'b'])
    expect(graph.nodes.find((n) => n.id === 'cond:a')?.type).toBe('condition')
    expect(summary(graph)).toEqual([
      'start->a',
      'a->cond:a',
      // Yes is emitted first, so the layout puts it on the left.
      'cond:a:yes->c',
      'cond:a:no->b',
      'b->c',
      'c->d',
      'd->stop:d:out',
    ])
    // The wait on an exit is the target's own (it starts once the answer is known).
    expect(graph.edges.find((e) => e.id === 'cond:a:yes->c')?.data?.label).toBe('Wait 2 days')
  })

  test('an exit left empty ends that path at its own Stop', () => {
    const graph = buildSequenceGraph(four, options({ branches: new Map([['b', branch('b', { yes_step_id: 'd' })]]) }))
    expect(summary(graph)).toContain('cond:b:no->stop:cond:b:no')
    expect(summary(graph)).toContain('cond:b:yes->d')
    // A condition's end can't be "inserted into": its exits are chosen, not placed.
    expect(graph.edges.find((e) => e.target === 'stop:cond:b:no')?.data?.insertLabel).toBeUndefined()
    // Step d's own fall-through end still can.
    expect(graph.edges.find((e) => e.target === 'stop:d:out')?.data?.insertLabel).toBe('Add a step after step 4')
  })

  test('both exits empty: two Stops, one per exit', () => {
    const graph = buildSequenceGraph(four, options({ branches: new Map([['d', branch('d', {})]]) }))
    const stops = graph.nodes.filter((n) => n.type === 'stop').map((n) => n.id)
    expect(stops).toEqual(['stop:cond:d:yes', 'stop:cond:d:no'])
  })

  test('two paths can converge on one step', () => {
    const graph = buildSequenceGraph(
      four,
      options({ branches: new Map([['a', branch('a', { yes_step_id: 'd', no_step_id: 'd' })]]) }),
    )
    expect(graph.edges.filter((e) => e.target === 'd').map((e) => e.sourceHandle)).toEqual(['yes', 'no', null])
  })

  test('"always" is its own kind with one exit', () => {
    const graph = buildSequenceGraph(
      four,
      options({ branches: new Map([['a', branch('a', { condition: 'always', within_days: null, yes_step_id: 'd' })]]) }),
    )
    expect(graph.nodes.find((n) => n.id === 'cond:a')?.type).toBe('always')
    expect(summary(graph).filter((e) => e.startsWith('cond:a'))).toEqual(['cond:a:yes->d'])
  })

  test('steps no path reaches are marked, and inserts appear only on fall-through edges', () => {
    const graph = buildSequenceGraph(
      four,
      options({ branches: new Map([['a', branch('a', { condition: 'always', within_days: null, yes_step_id: 'd' })]]) }),
    )
    const reach = Object.fromEntries(graph.nodes.flatMap((n) => (n.type === 'step' ? [[n.id, n.data.reachable]] : [])))
    expect(reach).toEqual({ a: true, b: false, c: false, d: true })
    const inserts = graph.edges.filter((e) => e.data?.insertLabel).map((e) => e.data?.insertLabel)
    // No "+" between a and its condition, nor on the condition's exit.
    expect(inserts).toEqual([
      'Add a step at the start',
      'Add a step after step 2',
      'Add a step after step 3',
      'Add a step after step 4',
    ])
  })

  test('an exit naming a step not in the list ends the path, as the send path does', () => {
    const graph = buildSequenceGraph(four, options({ branches: new Map([['a', branch('a', { yes_step_id: 'gone' })]]) }))
    expect(summary(graph)).toContain('cond:a:yes->stop:cond:a:yes')
  })

  test('loop steps are flagged on both the step and its condition', () => {
    const graph = buildSequenceGraph(
      four,
      options({
        branches: new Map([['c', branch('c', { condition: 'always', within_days: null, yes_step_id: 'a' })]]),
        loopStepIds: new Set(['a', 'b', 'c']),
      }),
    )
    const flagged = graph.nodes.flatMap((n) => ('inLoop' in n.data && n.data.inLoop ? [n.id] : []))
    expect(flagged).toEqual(['a', 'b', 'c', 'cond:c'])
  })

  test('branchesByStep keeps only steps that have one', () => {
    const map = branchesByStep([
      { step_id: 'a', branch: branch('a', {}) },
      { step_id: 'b', branch: null },
    ])
    expect([...map.keys()]).toEqual(['a'])
    expect(branchesByStep(undefined).size).toBe(0)
  })
})

describe('exitForConnection', () => {
  const ids = new Set(['a', 'b', 'c'])
  const branches = new Map([
    ['a', branch('a', {})],
    ['b', branch('b', { condition: 'always', within_days: null })],
  ])
  const drag = (source: string, target: string, sourceHandle: string | null) =>
    exitForConnection({ source, target, sourceHandle }, branches, ids)

  test('a drag from a condition exit onto another step sets that exit', () => {
    expect(drag('cond:a', 'c', 'yes')).toEqual({ stepId: 'a', exit: 'yes', target: 'c' })
    expect(drag('cond:a', 'b', 'no')).toEqual({ stepId: 'a', exit: 'no', target: 'b' })
  })

  test('refuses its own step, non-steps, a missing branch, and the No exit "always" does not have', () => {
    expect(drag('cond:a', 'a', 'yes')).toBeNull()
    expect(drag('cond:a', 'stop:x', 'yes')).toBeNull()
    expect(drag('cond:c', 'a', 'yes')).toBeNull()
    expect(drag('cond:b', 'a', 'no')).toBeNull()
    expect(drag('cond:b', 'a', 'yes')).toEqual({ stepId: 'b', exit: 'yes', target: 'a' })
    // A drag from a step or Start is a reorder, not an exit.
    expect(drag('a', 'c', null)).toBeNull()
  })
})

describe('step order', () => {
  const order = ['a', 'b', 'c']

  test('placeAfter moves an id directly after the anchor, or first for null', () => {
    expect(placeAfter(order, 'a', 'c')).toEqual(['a', 'c', 'b'])
    expect(placeAfter(order, null, 'c')).toEqual(['c', 'a', 'b'])
    expect(placeAfter([...order, 'new'], 'a', 'new')).toEqual(['a', 'new', 'b', 'c'])
  })

  test('placeAfter leaves the order alone for a stale anchor', () => {
    expect(placeAfter(order, 'gone', 'c')).toEqual(order)
  })

  test('moveStep swaps with a neighbour and is a no-op at either end', () => {
    expect(moveStep(order, 'b', -1)).toEqual(['b', 'a', 'c'])
    expect(moveStep(order, 'b', 1)).toEqual(['a', 'c', 'b'])
    expect(moveStep(order, 'a', -1)).toEqual(order)
    expect(moveStep(order, 'c', 1)).toEqual(order)
    expect(moveStep(order, 'missing', 1)).toEqual(order)
  })

  test('a connection from Start makes the target first; from a step, next', () => {
    expect(orderForConnection(order, START_NODE_ID, 'c')).toEqual(['c', 'a', 'b'])
    expect(orderForConnection(order, 'a', 'c')).toEqual(['a', 'c', 'b'])
  })

  test('only Start/step → a different step is a meaningful connection', () => {
    const ids = new Set(order)
    expect(isMeaningfulConnection({ source: 'a', target: 'c' }, ids)).toBe(true)
    expect(isMeaningfulConnection({ source: START_NODE_ID, target: 'b' }, ids)).toBe(true)
    expect(isMeaningfulConnection({ source: 'a', target: 'a' }, ids)).toBe(false)
    expect(isMeaningfulConnection({ source: 'a', target: 'stop:c:out' }, ids)).toBe(false)
    expect(isMeaningfulConnection({ source: 'stop:c:out', target: 'a' }, ids)).toBe(false)
  })

  test('leadsWithSubject refuses an order that opens on a blank subject', () => {
    const subjects = new Map([
      ['a', 'Intro'],
      ['b', ''],
      ['c', '   '],
    ])
    const subjectOf = (id: string) => subjects.get(id)
    expect(leadsWithSubject(['a', 'b'], subjectOf)).toBe(true)
    expect(leadsWithSubject(['b', 'a'], subjectOf)).toBe(false)
    // Whitespace is blank too — it would send as "Re:" of nothing.
    expect(leadsWithSubject(['c', 'a'], subjectOf)).toBe(false)
    expect(leadsWithSubject([], subjectOf)).toBe(true)
  })

  test('orderChanged', () => {
    expect(orderChanged(order, [...order])).toBe(false)
    expect(orderChanged(order, ['b', 'a', 'c'])).toBe(true)
    expect(orderChanged(order, [...order, 'd'])).toBe(true)
  })
})
