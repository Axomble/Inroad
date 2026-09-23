import { createContext, useContext } from 'react'
import type { ClassifyVariable, MergeVariable } from './merge-tags'

type VariableContextValue = { variables: MergeVariable[]; classify: ClassifyVariable }

/**
 * How chips learn whether their token resolves. Context rather than node
 * attributes, because the answer changes without the document changing — the
 * custom-field list loading turns every `{{custom.*}}` chip from unverified to
 * known or unknown at once. TipTap renders React node views through portals
 * inside `EditorContent`, so they see providers above it.
 */
export const VariableContext = createContext<VariableContextValue>({
  variables: [],
  classify: () => 'unverified',
})

export function useVariableContext(): VariableContextValue {
  return useContext(VariableContext)
}
