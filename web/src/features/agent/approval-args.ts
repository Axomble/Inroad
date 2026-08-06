/**
 * The editable model behind an approval card.
 *
 * A consequential action is reviewed on what it will DO, not on the JSON the
 * model happened to emit — so each shipped consequential tool gets a typed
 * draft with named fields, and unknown tools fall back to raw JSON. Keys the
 * renderer doesn't know about (`loading_message`, anything a newer tool adds)
 * ride along in `extra` so editing one field never silently drops another.
 */

export type ToolArguments = Record<string, unknown>

export interface ContactRow {
  email: string
  first_name?: string
  last_name?: string
  company?: string
}

/**
 * A row while it is being edited. `key` is client-only and never submitted: it
 * keeps React's identity stable when a row above it is removed, which an index
 * key cannot do.
 */
export interface DraftContact extends ContactRow {
  key: string
}

export type EditDraft =
  | { tool: 'inroad_campaign_control'; method: string; campaignId: string; extra: ToolArguments }
  | { tool: 'inroad_contacts_import'; listId: string; contacts: DraftContact[]; extra: ToolArguments }
  | { tool: 'json'; text: string }

export type DraftResult =
  | { ok: true; value: ToolArguments }
  | { ok: false; message: string }

export const campaignMethods = ['pause', 'resume'] as const

export function isJSONObject(value: unknown): value is ToolArguments {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** True when this tool has a purpose-built rendered view rather than the JSON fallback. */
export function hasRenderedView(toolName: string): boolean {
  return toolName === 'inroad_campaign_control' || toolName === 'inroad_contacts_import'
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function omit(source: ToolArguments, keys: string[]): ToolArguments {
  return Object.fromEntries(Object.entries(source).filter(([key]) => !keys.includes(key)))
}

/** Copies the row without its client-only key, dropping columns the row never had. */
function submittableRow(row: DraftContact): ContactRow {
  const result: ContactRow = { email: row.email.trim() }
  if (row.first_name) result.first_name = row.first_name
  if (row.last_name) result.last_name = row.last_name
  if (row.company) result.company = row.company
  return result
}

function draftContacts(value: unknown): DraftContact[] {
  if (!Array.isArray(value)) return []
  return value.filter(isJSONObject).map((row, index) => {
    const contact: DraftContact = { key: `row-${index}`, email: text(row.email) }
    if (text(row.first_name)) contact.first_name = text(row.first_name)
    if (text(row.last_name)) contact.last_name = text(row.last_name)
    if (text(row.company)) contact.company = text(row.company)
    return contact
  })
}

export function createDraft(toolName: string, args: ToolArguments): EditDraft {
  if (toolName === 'inroad_campaign_control') {
    return {
      tool: toolName,
      method: text(args.method),
      campaignId: text(args.campaign_id),
      extra: omit(args, ['method', 'campaign_id']),
    }
  }
  if (toolName === 'inroad_contacts_import') {
    return {
      tool: toolName,
      listId: text(args.list_id),
      contacts: draftContacts(args.contacts),
      extra: omit(args, ['list_id', 'contacts']),
    }
  }
  return { tool: 'json', text: JSON.stringify(args, null, 2) }
}

const emailPattern = /^[^\s@]+@[^\s@.]+\.[^\s@]+$/

/** Validates a draft and produces the argument object to submit. */
export function draftArguments(draft: EditDraft): DraftResult {
  if (draft.tool === 'json') {
    try {
      const parsed: unknown = JSON.parse(draft.text)
      if (!isJSONObject(parsed)) return { ok: false, message: 'Edited arguments must be a JSON object.' }
      return { ok: true, value: parsed }
    } catch {
      return { ok: false, message: 'Edited arguments must be a valid JSON object.' }
    }
  }

  if (draft.tool === 'inroad_campaign_control') {
    if (!campaignMethods.includes(draft.method as (typeof campaignMethods)[number])) {
      return { ok: false, message: 'Choose whether to pause or resume the campaign.' }
    }
    if (!draft.campaignId.trim()) return { ok: false, message: 'A campaign id is required.' }
    return {
      ok: true,
      value: { ...draft.extra, method: draft.method, campaign_id: draft.campaignId.trim() },
    }
  }

  if (!draft.listId.trim()) return { ok: false, message: 'A target list id is required.' }
  if (draft.contacts.length === 0) {
    return { ok: false, message: 'Keep at least one contact, or reject the action instead.' }
  }
  const badRow = draft.contacts.findIndex((row) => !emailPattern.test(row.email.trim()))
  if (badRow >= 0) {
    return { ok: false, message: `Row ${badRow + 1} needs a valid email address.` }
  }
  return {
    ok: true,
    value: {
      ...draft.extra,
      list_id: draft.listId.trim(),
      contacts: draft.contacts.map(submittableRow),
    },
  }
}

export interface ArgumentChange {
  key: string
  before: string
  after: string
}

/** A human-readable rendering of one argument value, for the before/after diff. */
export function describeValue(value: unknown): string {
  if (value === undefined) return 'not set'
  if (Array.isArray(value)) return `${value.length} ${value.length === 1 ? 'row' : 'rows'}`
  if (typeof value === 'string') return value || 'empty'
  return JSON.stringify(value)
}

/** Top-level argument keys whose value changed, in a stable order. */
export function diffArguments(before: ToolArguments, after: ToolArguments): ArgumentChange[] {
  const keys = [...new Set([...Object.keys(before), ...Object.keys(after)])].sort()
  return keys
    .filter((key) => JSON.stringify(before[key]) !== JSON.stringify(after[key]))
    .map((key) => ({
      key,
      before: describeValue(before[key]),
      after: describeValue(after[key]),
    }))
}
