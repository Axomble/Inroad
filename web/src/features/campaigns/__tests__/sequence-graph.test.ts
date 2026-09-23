import { describe, expect, test } from 'vitest'
import {
  START_NODE_ID,
  buildSequenceGraph,
  isMeaningfulConnection,
  leadsWithSubject,
  moveStep,
  orderChanged,
  orderForConnection,
  placeAfter,
} from '../sequence-graph'
import { sequenceNodeRegistry } from '../sequence-node-registry'
import type { StepWithId } from '../step-card'

const steps: StepWithId[] = [
  { id: 's-1', step_order: 1, delay_seconds: 0, subject: 'Intro' },
  { id: 's-2', step_order: 2, delay_seconds: 3 * 86400, subject: '' },
]
const outputsOf = sequenceNodeRegistry.outputsOf

describe('buildSequenceGraph', () => {
  test('Start → each step in order → one Stop after the last', () => {
    const { nodes, edges } = buildSequenceGraph(steps, { canModifyStructure: true, outputsOf })
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
    const { edges } = buildSequenceGraph(steps, { canModifyStructure: true, outputsOf })
    expect(edges.map((e) => e.data?.label)).toEqual(['No wait', 'Wait 3 days', undefined])
  })

  test('every edge offers an insert while the structure is editable, named by position', () => {
    const editable = buildSequenceGraph(steps, { canModifyStructure: true, outputsOf })
    expect(editable.edges.map((e) => e.data?.insertLabel)).toEqual([
      'Add a step at the start',
      'Add a step after step 1',
      'Add a step after step 2',
    ])
    const locked = buildSequenceGraph(steps, { canModifyStructure: false, outputsOf })
    expect(locked.edges.every((e) => e.data?.insertLabel === undefined)).toBe(true)
  })

  test('a step node knows its position and the thread subject for blank follow-ups', () => {
    const { nodes } = buildSequenceGraph(steps, { canModifyStructure: false, outputsOf })
    const second = nodes.find((n) => n.id === 's-2')
    expect(second?.type === 'step' && second.data).toMatchObject({
      position: 2,
      threadSubject: 'Intro',
      canModifyStructure: false,
    })
  })

  test('with no steps, Start runs straight to Stop', () => {
    const { nodes, edges } = buildSequenceGraph([], { canModifyStructure: true, outputsOf })
    expect(nodes.map((n) => n.type)).toEqual(['start', 'stop'])
    expect(edges).toHaveLength(1)
    expect(edges[0]?.data?.insertLabel).toBe('Add a step at the start')
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
