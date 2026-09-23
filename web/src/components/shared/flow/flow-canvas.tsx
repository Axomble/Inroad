import { useCallback, useEffect } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  ReactFlowProvider,
  getNodesBounds,
  useReactFlow,
  useStore,
  type Connection,
  type Edge,
  type IsValidConnection,
  type Node,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import './flow.css'
import { cn } from '@/lib/utils'
import { FlowEdge, type FlowEdgeType } from './flow-edge'
import { FlowInsertContext, type FlowInsertTarget } from './flow-insert-context'
import { FlowRegistryContext } from './flow-registry-context'
import type { FlowNodeRegistry } from './node-registry'

// Module scope on purpose: React Flow warns (and re-mounts every edge) when the
// `edgeTypes` object is a new identity on each render.
const edgeTypes = { flow: FlowEdge }
// The arrowhead is its own SVG marker, which React Flow colours inline — give it
// the edge's token so it follows the theme like the stroke does.
const defaultEdgeOptions = {
  type: 'flow',
  markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: 'var(--border-strong)' },
}

const FIT_PADDING = 32
const MAX_FIT_ZOOM = 1
/**
 * Below this a card's 12px text stops being readable. A graph that would need
 * to shrink further to fit is shown at this zoom from its top instead, and the
 * rest is reached by panning — or by Tab, which pans to the focused node.
 */
const MIN_READABLE_ZOOM = 0.75

export type FlowCanvasProps<N extends Node> = {
  /** Already laid out — see `useFlowLayout`. The canvas never moves a node itself. */
  nodes: N[]
  edges: FlowEdgeType[]
  /** The same registry the nodes were laid out with. */
  registry: FlowNodeRegistry
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
  style?: React.CSSProperties
}

/**
 * The domain-free canvas shell: pan, zoom controls, the dotted ground, the one
 * edge type, a readable fit whenever nodes come or go, and panning to whatever
 * node receives keyboard focus. Layout is automatic, so nodes are not
 * draggable; interaction lives in the buttons each node renders, which keeps it
 * reachable from the keyboard.
 *
 * Wheel scrolling is left to the page (a canvas embedded in a scrolling page
 * that swallows the wheel is a trap); zoom is on the controls and pinch.
 */
export function FlowCanvas<N extends Node>(props: FlowCanvasProps<N>) {
  return (
    <ReactFlowProvider>
      <FlowCanvasInner {...props} />
    </ReactFlowProvider>
  )
}

function FlowCanvasInner<N extends Node>({
  nodes,
  edges,
  registry,
  ariaLabel,
  onInsert,
  onConnect,
  isValidConnection,
  className,
  style,
}: FlowCanvasProps<N>) {
  const onFocus = usePanToFocusedNode()
  return (
    <section
      aria-label={ariaLabel}
      className={cn('inroad-flow relative h-full w-full', className)}
      style={style}
      onFocus={onFocus}
    >
      <FlowRegistryContext.Provider value={registry}>
        <FlowInsertContext.Provider value={onInsert ?? null}>
          <ReactFlow<N, Edge>
            nodes={nodes}
            edges={edges}
            nodeTypes={registry.nodeTypes}
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
          >
            <Background variant={BackgroundVariant.Dots} gap={18} size={1.2} />
            <Controls showInteractive={false} position="bottom-left" />
            <ReadableFit signature={nodes.map((node) => node.id).join('|')} />
          </ReactFlow>
        </FlowInsertContext.Provider>
      </FlowRegistryContext.Provider>
    </section>
  )
}

/**
 * Brings a node into view when something inside it takes focus and it isn't
 * already fully visible — otherwise Tab walks into nodes the viewport has
 * scrolled past, and a keyboard user can't see what they are operating.
 */
function usePanToFocusedNode() {
  const { getInternalNode, getViewport, setCenter } = useReactFlow()
  const paneWidth = useStore((state) => state.width)
  const paneHeight = useStore((state) => state.height)
  return useCallback(
    (event: React.FocusEvent<HTMLElement>) => {
      const id = event.target instanceof Element ? event.target.closest('.react-flow__node')?.getAttribute('data-id') : null
      const node = id ? getInternalNode(id) : undefined
      if (!node) return
      const width = node.measured.width ?? node.width ?? 0
      const height = node.measured.height ?? node.height ?? 0
      const { x, y } = node.internals.positionAbsolute
      // Visibility in flow coordinates, not DOM rects: by the time this runs the
      // browser has already scrolled React Flow's overflow-hidden wrapper to
      // reveal the focused element (React Flow resets that scroll right after),
      // so the DOM briefly claims the node is on screen when it isn't.
      const viewport = getViewport()
      const left = x * viewport.zoom + viewport.x
      const top = y * viewport.zoom + viewport.y
      const visible =
        left >= 0 &&
        top >= 0 &&
        left + width * viewport.zoom <= paneWidth &&
        top + height * viewport.zoom <= paneHeight
      if (visible) return
      void setCenter(x + width / 2, y + height / 2, {
        zoom: viewport.zoom,
        duration: prefersReducedMotion() ? 0 : 200,
      })
    },
    [getInternalNode, getViewport, paneHeight, paneWidth, setCenter],
  )
}

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

/**
 * Fits the graph when its nodes change (not when one merely moves — that would
 * yank the viewport away from the node a user is reordering) or the pane
 * resizes. A graph too tall to fit at a readable zoom is shown from the top.
 */
function ReadableFit({ signature }: { signature: string }) {
  const { fitView, setViewport, getNodes } = useReactFlow()
  const width = useStore((state) => state.width)
  const height = useStore((state) => state.height)
  useEffect(() => {
    // Nodes arrive pre-sized from the layout, so there is nothing to wait for
    // but the pane itself (React Flow's `useNodesInitialized` waits on a DOM
    // measurement pass that pre-sized nodes never trigger).
    if (width === 0 || height === 0) return
    const bounds = getNodesBounds(getNodes())
    if (bounds.width === 0 || bounds.height === 0) return
    const fitZoom = Math.min(
      (width - 2 * FIT_PADDING) / bounds.width,
      (height - 2 * FIT_PADDING) / bounds.height,
      MAX_FIT_ZOOM,
    )
    if (fitZoom >= MIN_READABLE_ZOOM) {
      void fitView({ padding: `${FIT_PADDING}px`, maxZoom: MAX_FIT_ZOOM })
      return
    }
    const zoom = MIN_READABLE_ZOOM
    void setViewport({
      x: (width - bounds.width * zoom) / 2 - bounds.x * zoom,
      y: FIT_PADDING - bounds.y * zoom,
      zoom,
    })
  }, [fitView, getNodes, height, setViewport, signature, width])
  return null
}
