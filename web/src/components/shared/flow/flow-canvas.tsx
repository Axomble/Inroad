import { useEffect } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  useNodesInitialized,
  useReactFlow,
  type Connection,
  type Edge,
  type IsValidConnection,
  type Node,
  type NodeTypes,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import './flow.css'
import { cn } from '@/lib/utils'
import { FlowEdge, type FlowEdgeType } from './flow-edge'
import { FlowInsertContext, type FlowInsertTarget } from './flow-insert-context'

// Module scope on purpose: React Flow warns (and re-mounts every edge) when the
// `edgeTypes` object is a new identity on each render.
const edgeTypes = { flow: FlowEdge }
// The arrowhead is its own SVG marker, which React Flow colours inline — give it
// the edge's token so it follows the theme like the stroke does.
const defaultEdgeOptions = {
  type: 'flow',
  markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: 'var(--border-strong)' },
}
const fitViewOptions = { padding: 0.2, maxZoom: 1 }

export type FlowCanvasProps<N extends Node> = {
  /** Already laid out — see `useFlowLayout`. The canvas never moves a node itself. */
  nodes: N[]
  edges: FlowEdgeType[]
  /** From the caller's registry (`defineFlowNodeTypes(...).nodeTypes`). */
  nodeTypes: NodeTypes
  /** Names the canvas region for assistive tech ("Sequence flow"). */
  ariaLabel: string
  /** Wires every edge's add button. Omit and no edge offers one. */
  onInsert?: (target: FlowInsertTarget) => void
  /**
   * Makes handles draggable. Nothing ever connects on its own: an edge only
   * exists because the graph the caller built says so, and a drag only asks the
   * caller to change that graph.
   */
  onConnect?: (connection: Connection) => void
  isValidConnection?: IsValidConnection
  className?: string
}

/**
 * The domain-free canvas shell: pan, zoom controls, the dotted ground, the one
 * edge type, and fit-to-content whenever the graph changes shape. Layout is
 * automatic, so nodes are not draggable; interaction lives in the buttons each
 * node renders, which keeps it reachable from the keyboard.
 *
 * Wheel scrolling is left to the page (a canvas embedded in a scrolling page
 * that swallows the wheel is a trap); zoom is on the controls and pinch.
 */
export function FlowCanvas<N extends Node>({
  nodes,
  edges,
  nodeTypes,
  ariaLabel,
  onInsert,
  onConnect,
  isValidConnection,
  className,
}: FlowCanvasProps<N>) {
  return (
    <section aria-label={ariaLabel} className={cn('inroad-flow relative h-full w-full', className)}>
      <FlowInsertContext.Provider value={onInsert ?? null}>
        <ReactFlow<N, Edge>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          defaultEdgeOptions={defaultEdgeOptions}
          nodesDraggable={false}
          nodesConnectable={onConnect !== undefined}
          nodesFocusable={false}
          edgesFocusable={false}
          elementsSelectable={false}
          onConnect={onConnect}
          isValidConnection={isValidConnection}
          zoomOnScroll={false}
          preventScrolling={false}
          minZoom={0.3}
          maxZoom={1.5}
          fitView
          fitViewOptions={fitViewOptions}
        >
          <Background variant={BackgroundVariant.Dots} gap={18} size={1.2} />
          <Controls showInteractive={false} position="bottom-left" />
          <RefitOnGraphChange signature={graphSignature(nodes)} />
        </ReactFlow>
      </FlowInsertContext.Provider>
    </section>
  )
}

/** Changes exactly when a node appears, disappears, or moves. */
function graphSignature(nodes: readonly Node[]): string {
  return nodes.map((node) => `${node.id}@${node.position.x},${node.position.y}`).join('|')
}

/**
 * `fitView` on the ReactFlow element only runs once, at mount; adding a step
 * would otherwise grow the graph off the bottom of the viewport.
 */
function RefitOnGraphChange({ signature }: { signature: string }) {
  const { fitView } = useReactFlow()
  const initialized = useNodesInitialized()
  useEffect(() => {
    if (!initialized) return
    void fitView(fitViewOptions)
  }, [fitView, initialized, signature])
  return null
}
