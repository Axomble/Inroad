// Pure display/validation helpers shared by the provider components and the
// page's model grouping — kept out of the .tsx files so those only export
// components (fast-refresh rule).
import type { AiProvider, AiProviderConfig } from './api'
import { KIND_META, type ProviderFormField } from './provider-kinds'

/** Accepts only absolute http(s) URLs — what the backend accepts for URL fields. */
export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

/** A JSON object (a service-account key file), not just any parseable JSON. */
export function isJsonObject(value: string): boolean {
  try {
    const parsed: unknown = JSON.parse(value)
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
  } catch {
    return false
  }
}

/**
 * Whether one connect-form field holds an acceptable value. `requireSecrets`
 * is false when editing: blank secrets mean "keep what's stored".
 */
export function fieldValid(field: ProviderFormField, raw: string | undefined, requireSecrets: boolean): boolean {
  const value = raw?.trim() ?? ''
  if (value === '') return !field.required || (field.secret && !requireSecrets)
  if (field.input === 'url') return isHttpUrl(value)
  if (field.input === 'json') return isJsonObject(value)
  return true
}

/**
 * A human handle for a provider row: its display name, else its base URL host
 * (gateways), else the kind's title.
 */
export function providerTitle(provider: AiProvider): string {
  if (provider.display_name.trim()) return provider.display_name
  const baseUrl = provider.config.base_url
  if (provider.kind === 'openai_compatible' && baseUrl) {
    try {
      return new URL(baseUrl).host
    } catch {
      return baseUrl
    }
  }
  return KIND_META[provider.kind]?.title ?? provider.kind
}

/**
 * One seam for reading a config value by a form-field key. The generated
 * `AiProviderConfig` has named optional props (no index signature), while the
 * connect-form schema addresses fields by string key — this is the single
 * documented cast bridging the two.
 */
export function configValue(config: AiProviderConfig | undefined, key: string): string {
  return (config as Record<string, string | undefined> | undefined)?.[key] ?? ''
}

/**
 * The non-secret facts worth showing on a provider row, in display order —
 * the row's config chips.
 */
export function configSummary(provider: AiProvider): string[] {
  const { base_url, endpoint, region, project_id, api_version } = provider.config
  return [base_url, endpoint, region, project_id, api_version].filter((v): v is string => Boolean(v))
}

/** e.g. 200000 → "200k tokens". */
export function formatTokens(tokens: number): string {
  return tokens >= 1000 ? `${Math.round(tokens / 1000)}k tokens` : `${tokens} tokens`
}
