import type { Edge, Node } from '@xyflow/react'
import { expect, test } from 'vitest'
import { NO_OUTPUTS, SINGLE_OUTPUT, defineFlowNodeTypes, openEnds } from '../node-registry'

const Stub = () => null

const registry = defineFlowNodeTypes({
  trigger: { component: Stub, size: { width: 100, height: 40 }, outputs: SINGLE_OUTPUT },
  condition: { component: Stub, size: { width: 160, height: 60 }, outputs: ['yes', 'no'] },
  end: { component: Stub, size: { width: 80, height: 30 }, outputs: NO_OUTPUTS },
})

const node = (id: string, type: string): Node => ({ id, type, position: { x: 0, y: 0 }, data: {} })

test('exposes one component per registered kind for React Flow', () => {
  expect(Object.keys(registry.nodeTypes)).toEqual(['trigger', 'condition', 'end'])
  expect(registry.sizeOf('condition')).toEqual({ width: 160, height: 60 })
  expect(registry.outputsOf('condition')).toEqual(['yes', 'no'])
})

test('an unregistered kind fails loudly instead of rendering a default box', () => {
  expect(() => registry.sizeOf('mystery')).toThrow(/no node type registered for "mystery"/)
  expect(() => registry.outputsOf(undefined)).toThrow()
})

test('openEnds reports each exit nothing leaves from, per named handle', () => {
  const nodes = [node('t', 'trigger'), node('if', 'condition'), node('x', 'end')]
  const edges: Edge[] = [
    { id: 't->if', source: 't', target: 'if' },
    { id: 'if-yes->x', source: 'if', sourceHandle: 'yes', target: 'x' },
  ]
  // The trigger is wired, the terminal has no exits, the condition's "no" is open.
  expect(openEnds(nodes, edges, registry.outputsOf)).toEqual([{ nodeId: 'if', handle: 'no' }])
})

test('openEnds treats a missing sourceHandle as the default exit', () => {
  const nodes = [node('a', 'trigger'), node('b', 'trigger')]
  const edges: Edge[] = [{ id: 'a->b', source: 'a', target: 'b', sourceHandle: null }]
  expect(openEnds(nodes, edges, registry.outputsOf)).toEqual([{ nodeId: 'b', handle: null }])
})
