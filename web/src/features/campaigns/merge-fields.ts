import { useMemo } from 'react'
import type { ClassifyVariable, MergeVariable, VariableStatus } from '@/components/shared/rich-text-editor'
// Read-only reference data from another feature's API (hooks only — see
// CLAUDE.md): the workspace's custom field definitions.
import { useListCustomFieldsQuery } from '@/features/contacts/api'

/**
 * The fixed placeholders the send path fills from the contact's own columns —
 * `builtinTokens` in internal/app/campaign/tokens.go, which that package's
 * tests hold equal to internal/worker/personalize/personalize.go.
 */
export const BUILTIN_MERGE_FIELDS: MergeVariable[] = [
  { name: 'first_name', label: 'First name' },
  { name: 'last_name', label: 'Last name' },
  { name: 'email', label: 'Email' },
  { name: 'company', label: 'Company' },
]

const CUSTOM_PREFIX = 'custom.'

/**
 * The same rule as the backend's launch check (`tokenResolves`): a builtin, or
 * `custom.<key>` for a LIVE custom field. Case-sensitive and exact — the send
 * path does a literal replace, so `{{ first_name }}` is unknown. `customKeys`
 * is null while the definitions aren't loaded; custom tokens are then
 * unverified rather than wrongly blamed.
 */
export function classifyMergeField(name: string, customKeys: ReadonlySet<string> | null): VariableStatus {
  if (BUILTIN_MERGE_FIELDS.some((field) => field.name === name)) return 'known'
  if (!name.startsWith(CUSTOM_PREFIX)) return 'unknown'
  if (customKeys === null) return 'unverified'
  return customKeys.has(name.slice(CUSTOM_PREFIX.length)) ? 'known' : 'unknown'
}

/** Everything a step's editors offer and check against: builtins plus live custom fields. */
export function useMergeFields(): { variables: MergeVariable[]; classify: ClassifyVariable } {
  const { data } = useListCustomFieldsQuery()
  return useMemo(() => {
    // Anything but a list — still loading, failed, or a malformed body — means
    // the definitions are unknown, so custom tokens stay unverified instead of
    // taking the form down or being blamed.
    const loaded = Array.isArray(data)
    // Archived fields no longer resolve in templates, so they are neither
    // offered nor accepted.
    const live = loaded ? data.filter((field) => !field.archived) : []
    const customKeys = loaded ? new Set(live.map((field) => field.key)) : null
    return {
      variables: [
        ...BUILTIN_MERGE_FIELDS,
        ...live.map((field) => ({ name: `${CUSTOM_PREFIX}${field.key}`, label: field.label })),
      ],
      classify: (name: string) => classifyMergeField(name, customKeys),
    }
  }, [data])
}
