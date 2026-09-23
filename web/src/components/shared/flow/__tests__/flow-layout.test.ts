import { renderHook } from '@testing-library/react'
import type { Node, XYPosition } from '@xyflow/react'
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

type Laid = ReturnType<typeof layoutFlow<Node>>

/** An edge as drawn: from its source's exit, through any route, to its target's entry. */
function drawn(laid: Laid, drawnEdge: FlowEdgeType): XYPosition[] {
  const byId = new Map(laid.nodes.map((n) => [n.id, n]))
  const source = byId.get(drawnEdge.source)
  const target = byId.get(drawnEdge.target)
  if (!source || !target) return []
  const exit = drawnEdge.sourceHandle === 'yes' ? 1 / 3 : drawnEdge.sourceHandle === 'no' ? 2 / 3 : 1 / 2
  const from = { x: source.position.x + (source.width ?? 0) * exit, y: source.position.y + (source.height ?? 0) }
  const to = { x: target.position.x + (target.width ?? 0) / 2, y: target.position.y }
  return [from, ...(drawnEdge.data?.route ?? []), to]
}

/** Where an exit heads first below its node: its first bend, or its target. */
function firstStepX(laid: Laid, exitEdge: FlowEdgeType): number {
  return drawn(laid, exitEdge)[1]?.x ?? Number.NaN
}

function segmentsCross([p1, p2]: [XYPosition, XYPosition], [p3, p4]: [XYPosition, XYPosition]): boolean {
  const side = (a: XYPosition, b: XYPosition, c: XYPosition) => (b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x)
  return side(p3, p4, p1) * side(p3, p4, p2) < 0 && side(p1, p2, p3) * side(p1, p2, p4) < 0
}

/** Every pair of drawn edges that properly cross (touching at a shared node doesn't count). */
function crossings(laid: Laid): string[] {
  const lines = laid.edges.map((e) => ({ id: e.id, points: drawn(laid, e) }))
  const found: string[] = []
  lines.forEach((a, i) => {
    for (const b of lines.slice(i + 1)) {
      const hit = a.points.slice(1).some((end, k) =>
        b.points.slice(1).some((otherEnd, m) =>
          segmentsCross([a.points[k] ?? end, end], [b.points[m] ?? otherEnd, otherEnd]),
        ),
      )
      if (hit) found.push(`${a.id} × ${b.id}`)
    }
  })
  return found
}

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

  test('an edge that skips a rank is routed round the nodes in between; adjacent ones are not', () => {
    // The long s → b1 → b2 → c path fixes the ranks, so one of the edges on the
    // short s → a → c path has to span more than one (dagre picks which).
    const nodes = ['s', 'a', 'b1', 'b2', 'c'].map((id) => node(id, 'action'))
    const laid = layoutFlow(
      nodes,
      [edge('s', 'a'), edge('s', 'b1'), edge('b1', 'b2'), edge('b2', 'c'), edge('a', 'c')],
      { sizeOf },
    )
    const routed = laid.edges.filter((e) => e.data?.route)
    expect(routed.map((e) => e.id)).toHaveLength(1)
    expect(laid.edges.find((e) => e.id === 'b1:->b2')?.data?.route).toBeUndefined()
    // No bend lands inside any node's box.
    for (const point of routed.flatMap((e) => e.data?.route ?? [])) {
      for (const n of laid.nodes) {
        const inside =
          point.x > n.position.x &&
          point.x < n.position.x + (n.width ?? 0) &&
          point.y > n.position.y &&
          point.y < n.position.y + (n.height ?? 0)
        expect(inside, `bend ${point.x},${point.y} inside ${n.id}`).toBe(false)
      }
    }
    expect(crossings(laid)).toEqual([])
  })

  test('a multi-exit node keeps its exits in edge order even when one skips a rank', () => {
    // Yes jumps past b to c; No goes straight to b. Uncrossed means the yes
    // path stays left of b the whole way, and b sits right of the fork.
    const nodes = [node('if', 'condition'), node('b', 'action'), node('c', 'action')]
    const laid = layoutFlow(nodes, [edge('if', 'c', 'yes'), edge('if', 'b', 'no'), edge('b', 'c')], { sizeOf })
    const b = laid.nodes.find((n) => n.id === 'b')
    const yesRoute = laid.edges.find((e) => e.id === 'if:yes->c')?.data?.route ?? []
    const passingB = yesRoute.filter((point) => point.y >= (b?.position.y ?? 0) && point.y <= (b?.position.y ?? 0) + 80)
    expect(passingB.length).toBeGreaterThan(0)
    for (const point of passingB) expect(point.x).toBeLessThan(b?.position.x ?? 0)
    expect(crossings(laid)).toEqual([])
  })

  test('two conditions, a converging path and a backward jump: no Yes/No inversion, no crossings', () => {
    // 1 → IF(yes 4, no 2); 2 → 3 → IF(yes 5, no 4)  — both conditions converge on 4
    // 4 → ALWAYS → end;    5 → 6 → ALWAYS(back to 4) — a jump backwards in order
    const nodes = [
      node('s1', 'action'),
      node('c1', 'condition'),
      node('s2', 'action'),
      node('s3', 'action'),
      node('c3', 'condition'),
      node('s4', 'action'),
      node('a4', 'trigger'),
      node('end', 'trigger'),
      node('s5', 'action'),
      node('s6', 'action'),
      node('a6', 'trigger'),
    ]
    const edges = [
      edge('s1', 'c1'),
      edge('c1', 's4', 'yes'),
      edge('c1', 's2', 'no'),
      edge('s2', 's3'),
      edge('s3', 'c3'),
      edge('c3', 's5', 'yes'),
      edge('c3', 's4', 'no'),
      edge('s4', 'a4'),
      edge('a4', 'end', 'yes'),
      edge('s5', 's6'),
      edge('s6', 'a6'),
      edge('a6', 's4', 'yes'),
    ]
    const laid = layoutFlow(nodes, edges, { sizeOf })
    for (const condition of ['c1', 'c3']) {
      const [yes, no] = ['yes', 'no'].map((handle) => {
        const exit = laid.edges.find((e) => e.source === condition && e.sourceHandle === handle)
        return exit ? firstStepX(laid, exit) : Number.NaN
      })
      expect(yes, `${condition}: yes leaves left of no`).toBeLessThan(no ?? Number.NaN)
    }
    expect(crossings(laid)).toEqual([])
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
