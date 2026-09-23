import { createContext, useContext } from 'react'
import type { FlowNodeRegistry } from './node-registry'

/**
 * The canvas's registry, for `FlowNodeFrame`: a node frame looks its own exits
 * up by type instead of being handed a list, so what it draws is by
 * construction what the layout and the edge router were told.
 */
export const FlowRegistryContext = createContext<FlowNodeRegistry | null>(null)

export function useFlowRegistry(): FlowNodeRegistry {
  const registry = useContext(FlowRegistryContext)
  if (!registry) throw new Error('flow: node frames must render inside a FlowCanvas')
  return registry
}
