import { createContext, useContext } from 'react'
import type { FlowHandleId } from './node-registry'

/** Where a user asked to put a new node: after `source`, on its `sourceHandle` exit. */
export type FlowInsertTarget = {
  source: string
  sourceHandle: FlowHandleId
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
