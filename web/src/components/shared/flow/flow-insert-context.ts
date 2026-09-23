import { createContext, useContext } from 'react'
import type { FlowHandleId } from './node-registry'

/** The edge a user asked to put a new node on: the new node goes after `source`. */
export type FlowInsertTarget = {
  edgeId: string
  source: string
  sourceHandle: FlowHandleId
  target: string
}

/**
 * How an edge's add button reaches the canvas owner. A context rather than a
 * callback in each edge's `data`, so the graph a caller builds stays plain data
 * and a re-created handler doesn't force a re-layout.
 */
export const FlowInsertContext = createContext<((target: FlowInsertTarget) => void) | null>(null)

export function useFlowInsert(): ((target: FlowInsertTarget) => void) | null {
  return useContext(FlowInsertContext)
}
