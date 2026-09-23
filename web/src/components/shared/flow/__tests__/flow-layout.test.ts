import { renderHook } from '@testing-library/react'
import type { Edge, Node } from '@xyflow/react'
import { describe, expect, test } from 'vitest'
import { layoutFlow, useFlowLayout } from '../flow-layout'
import type { FlowNodeSize } from '../node-registry'

const sizes: Record<string, FlowNodeSize> = {
  trigger: { width: 100, height: 40 },
  action: { width: 200, height: 80 },
  condition: { width: 160, height: 60 },
}
const sizeOf = (type: string | undefined): FlowNodeSize => {
  const size = type ? sizes[type] : undefined
  if (!size) throw new Error(`unsized ${String(type)}`)
  return size
}

const node = (id: string, type: string): Node => ({ id, type, position: { x: 0, y: 0 }, data: {} })
const edge = (source: string, target: string, sourceHandle?: string): Edge => ({
  id: `${source}->${target}`,
  source,
  target,
  sourceHandle,
})

describe('layoutFlow', () => {
  test('stacks a chain top to bottom, centred on one axis, at each node’s declared size', () => {
    const nodes = [node('a', 'trigger'), node('b', 'action'), node('c', 'action')]
    const laid = layoutFlow(nodes, [edge('a', 'b'), edge('b', 'c')], { sizeOf })

    const [a, b, c] = laid
    expect(a && b && c).toBeTruthy()
    if (!a || !b || !c) return
    // Each rank sits strictly below the previous one, with room between.
    expect(b.position.y).toBeGreaterThan(a.position.y + 40)
    expect(c.position.y).toBeGreaterThan(b.position.y + 80)
    // Position is the top-left corner: centres line up, not left edges.
    expect(a.position.x + 50).toBeCloseTo(b.position.x + 100)
    expect(b.position.x).toBeCloseTo(c.position.x)
    // Declared size is copied onto the node so React Flow needn't measure.
    expect(b).toMatchObject({ width: 200, height: 80 })
  })

  test('puts two branches side by side in one rank, first-emitted edge on the left', () => {
    const nodes = [node('if', 'condition'), node('yes', 'action'), node('no', 'action')]
    const laid = layoutFlow(nodes, [edge('if', 'yes', 'yes'), edge('if', 'no', 'no')], { sizeOf })
    const byId = new Map(laid.map((n) => [n.id, n]))
    const yes = byId.get('yes')
    const no = byId.get('no')
    expect(yes?.position.y).toBe(no?.position.y)
    expect((yes?.position.x ?? 0) + 200).toBeLessThanOrEqual(no?.position.x ?? 0)
  })

  test('the branch order follows the edges, not dagre’s own choice', () => {
    const nodes = [node('if', 'condition'), node('yes', 'action'), node('no', 'action')]
    const laid = layoutFlow(nodes, [edge('if', 'no', 'no'), edge('if', 'yes', 'yes')], { sizeOf })
    const byId = new Map(laid.map((n) => [n.id, n]))
    expect((byId.get('no')?.position.x ?? 0) + 200).toBeLessThanOrEqual(byId.get('yes')?.position.x ?? 0)
  })

  test('does not mutate its input and ignores edges to unknown nodes', () => {
    const nodes = [node('a', 'trigger')]
    const laid = layoutFlow(nodes, [edge('a', 'ghost')], { sizeOf })
    expect(nodes[0]?.position).toEqual({ x: 0, y: 0 })
    expect(nodes[0]?.width).toBeUndefined()
    expect(laid).toHaveLength(1)
  })

  test('LR direction lays ranks out horizontally', () => {
    const laid = layoutFlow([node('a', 'trigger'), node('b', 'action')], [edge('a', 'b')], { sizeOf }, {
      direction: 'LR',
    })
    expect(laid[1]?.position.x).toBeGreaterThan((laid[0]?.position.x ?? 0) + 100)
  })
})

describe('useFlowLayout', () => {
  test('returns the same array until the graph changes', () => {
    // Stable, like a module-scope registry.
    const shapes = { sizeOf }
    const nodes = [node('a', 'trigger'), node('b', 'action')]
    const edges = [edge('a', 'b')]
    const { result, rerender } = renderHook(({ n, e }) => useFlowLayout(n, e, shapes), {
      initialProps: { n: nodes, e: edges },
    })
    const first = result.current
    rerender({ n: nodes, e: edges })
    expect(result.current).toBe(first)

    rerender({ n: [...nodes, node('c', 'action')], e: [...edges, edge('b', 'c')] })
    expect(result.current).not.toBe(first)
    expect(result.current).toHaveLength(3)
  })
})
