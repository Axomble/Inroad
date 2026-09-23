import { renderHook } from '@testing-library/react'
import type { Node } from '@xyflow/react'
import type { FlowEdgeType } from '../flow-edge'
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
const edge = (source: string, target: string, sourceHandle?: string): FlowEdgeType => ({
  id: `${source}:${sourceHandle ?? ''}->${target}`,
  source,
  target,
  sourceHandle,
})

describe('layoutFlow', () => {
  test('stacks a chain top to bottom, centred on one axis, at each node’s declared size', () => {
    const nodes = [node('a', 'trigger'), node('b', 'action'), node('c', 'action')]
    const laid = layoutFlow(nodes, [edge('a', 'b'), edge('b', 'c')], { sizeOf }).nodes

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
    const laid = layoutFlow(nodes, [edge('if', 'yes', 'yes'), edge('if', 'no', 'no')], { sizeOf }).nodes
    const byId = new Map(laid.map((n) => [n.id, n]))
    const yes = byId.get('yes')
    const no = byId.get('no')
    expect(yes?.position.y).toBe(no?.position.y)
    expect((yes?.position.x ?? 0) + 200).toBeLessThanOrEqual(no?.position.x ?? 0)
  })

  test('the branch order follows the edges, not dagre’s own choice', () => {
    const nodes = [node('if', 'condition'), node('yes', 'action'), node('no', 'action')]
    const laid = layoutFlow(nodes, [edge('if', 'no', 'no'), edge('if', 'yes', 'yes')], { sizeOf }).nodes
    const byId = new Map(laid.map((n) => [n.id, n]))
    expect((byId.get('no')?.position.x ?? 0) + 200).toBeLessThanOrEqual(byId.get('yes')?.position.x ?? 0)
  })

  test('an edge that skips a rank is routed round the node in between; adjacent ones are not', () => {
    // a's rank is fixed by s → a, c's by the longer s → b1 → b2 → c, so a → c
    // has to cross b2's rank.
    const nodes = ['s', 'a', 'b1', 'b2', 'c'].map((id) => node(id, 'action'))
    const { nodes: laid, edges } = layoutFlow(
      nodes,
      [edge('s', 'a'), edge('s', 'b1'), edge('b1', 'b2'), edge('b2', 'c'), edge('a', 'c')],
      { sizeOf },
    )
    const byId = new Map(edges.map((e) => [e.id, e]))
    expect(byId.get('b1:->b2')?.data?.route).toBeUndefined()
    const route = byId.get('a:->c')?.data?.route ?? []
    expect(route.length).toBeGreaterThan(0)
    // The bend clears b2's box instead of running through it.
    const b2 = laid.find((n) => n.id === 'b2')
    const left = b2?.position.x ?? 0
    const top = b2?.position.y ?? 0
    const beside = route.filter((point) => point.y >= top && point.y <= top + 80)
    expect(beside.length).toBeGreaterThan(0)
    for (const point of beside) expect(point.x < left || point.x > left + 200).toBe(true)
  })

  test('a multi-exit node keeps its exits in edge order even when one skips a rank', () => {
    // Yes jumps past b to c; No goes straight to b. Uncrossed means the yes
    // path stays left of the no path on the way down.
    const nodes = [node('if', 'condition'), node('b', 'action'), node('c', 'action')]
    const { nodes: laid, edges } = layoutFlow(
      nodes,
      [edge('if', 'c', 'yes'), edge('if', 'b', 'no'), edge('b', 'c')],
      { sizeOf },
    )
    const [yesRelay] = edges.find((e) => e.id === 'if:yes->c')?.data?.route ?? []
    const [noRelay] = edges.find((e) => e.id === 'if:no->b')?.data?.route ?? []
    expect(yesRelay && noRelay).toBeTruthy()
    expect(yesRelay?.y).toBe(noRelay?.y)
    expect(yesRelay?.x ?? 0).toBeLessThan(noRelay?.x ?? 0)
    // …and stays left below the relays too: passing b, the yes path is on b's
    // left, not looping round its right and crossing the no path to get there.
    const b = laid.find((n) => n.id === 'b')
    const yesRoute = edges.find((e) => e.id === 'if:yes->c')?.data?.route ?? []
    const passingB = yesRoute.filter((point) => point.y >= (b?.position.y ?? 0) && point.y <= (b?.position.y ?? 0) + 80)
    expect(passingB.length).toBeGreaterThan(0)
    for (const point of passingB) expect(point.x).toBeLessThan(b?.position.x ?? 0)
  })

  test('two edges between the same pair are both kept', () => {
    const { edges } = layoutFlow(
      [node('if', 'condition'), node('x', 'action')],
      [edge('if', 'x', 'yes'), edge('if', 'x', 'no')],
      { sizeOf },
    )
    expect(edges.map((e) => e.id)).toEqual(['if:yes->x', 'if:no->x'])
  })

  test('does not mutate its input and ignores edges to unknown nodes', () => {
    const nodes = [node('a', 'trigger')]
    const laid = layoutFlow(nodes, [edge('a', 'ghost')], { sizeOf }).nodes
    expect(nodes[0]?.position).toEqual({ x: 0, y: 0 })
    expect(nodes[0]?.width).toBeUndefined()
    expect(laid).toHaveLength(1)
  })

  test('LR direction lays ranks out horizontally', () => {
    const laid = layoutFlow([node('a', 'trigger'), node('b', 'action')], [edge('a', 'b')], { sizeOf }, {
      direction: 'LR',
    }).nodes
    expect(laid[1]?.position.x).toBeGreaterThan((laid[0]?.position.x ?? 0) + 100)
  })
})

describe('useFlowLayout', () => {
  test('returns the same array until the graph changes', () => {
    // Stable, like a module-scope registry.
    const shapes = { sizeOf }
    const nodes = [node('a', 'trigger'), node('b', 'action')]
    const edges = [edge('a', 'b')]
    const { result, rerender } = renderHook(({ n, e }) => useFlowLayout(n, e, shapes).nodes, {
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
