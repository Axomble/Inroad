import { Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import {
  campaignMethods,
  type ArgumentChange,
  type EditDraft,
  type ToolArguments,
} from './approval-args'

/**
 * Rendered views for the shipped consequential tools. The point of the
 * approval gate is that a person reads what the action DOES; a JSON blob asks
 * them to be a parser. Unknown tools still fall back to JSON — better a raw
 * view than a wrong one.
 */

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2 border-t border-border px-3 py-2 first:border-t-0">
      <span className="w-28 shrink-0 text-[11px] text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 break-words text-[11px] font-medium text-foreground">{value}</span>
    </div>
  )
}

function JsonBlock({ value }: { value: ToolArguments }) {
  return (
    <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-background p-3 font-mono text-[11px] leading-5 text-muted-foreground">
      {JSON.stringify(value, null, 2)}
    </pre>
  )
}

function ContactTable({ contacts }: { contacts: ToolArguments['contacts'] }) {
  const rows = Array.isArray(contacts) ? contacts : []
  return (
    <div className="max-h-56 overflow-auto rounded-md border border-border bg-background">
      <table className="w-full text-left text-[11px]">
        <caption className="sr-only">Contacts that will be imported</caption>
        <thead className="sticky top-0 bg-surface text-muted-foreground">
          <tr>
            <th scope="col" className="px-2 py-1.5 font-medium">Email</th>
            <th scope="col" className="px-2 py-1.5 font-medium">Name</th>
            <th scope="col" className="px-2 py-1.5 font-medium">Company</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => {
            const contact = (row ?? {}) as Record<string, unknown>
            const name = [contact.first_name, contact.last_name].filter(Boolean).join(' ')
            return (
              // The read-only preview is a fixed ordered list — position IS the
              // row's identity here, and emails can legitimately repeat.
              // oxlint-disable-next-line no-array-index-key -- static list, position is the identity
              <tr key={index} className="border-t border-border">
                <td className="px-2 py-1.5 text-foreground">{String(contact.email ?? '')}</td>
                <td className="px-2 py-1.5 text-muted-foreground">{name || '—'}</td>
                <td className="px-2 py-1.5 text-muted-foreground">{String(contact.company ?? '') || '—'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/** One sentence describing the effect, above the details. */
export function ActionSummary({ toolName, args }: { toolName: string; args: ToolArguments }) {
  if (toolName === 'inroad_campaign_control') {
    const resuming = args.method === 'resume'
    return (
      <p className="text-xs leading-5 text-foreground">
        {resuming
          ? 'Resume this campaign — sending picks up where the schedule left off.'
          : 'Pause this campaign — new sends stop immediately.'}
      </p>
    )
  }
  if (toolName === 'inroad_contacts_import') {
    const count = Array.isArray(args.contacts) ? args.contacts.length : 0
    return (
      <p className="text-xs leading-5 text-foreground">
        Import {count} {count === 1 ? 'contact' : 'contacts'} into one existing contact list.
      </p>
    )
  }
  return (
    <p className="text-xs leading-5 text-muted-foreground">
      Review the exact inputs below before allowing this consequential action.
    </p>
  )
}

export function ApprovalPreview({ toolName, args }: { toolName: string; args: ToolArguments }) {
  if (toolName === 'inroad_campaign_control') {
    return (
      <div className="rounded-md border border-border bg-background">
        <Field label="Action" value={args.method === 'resume' ? 'Resume sending' : 'Pause sending'} />
        <Field label="Campaign" value={String(args.campaign_id ?? '')} />
      </div>
    )
  }
  if (toolName === 'inroad_contacts_import') {
    return (
      <div className="space-y-2">
        <div className="rounded-md border border-border bg-background">
          <Field label="Target list" value={String(args.list_id ?? '')} />
        </div>
        <ContactTable contacts={args.contacts} />
      </div>
    )
  }
  return <JsonBlock value={args} />
}

/**
 * Field-by-field editor. Bulk imports expose the email column and row removal
 * rather than an input per cell: those are the corrections a reviewer actually
 * makes, and one text input per field across hundreds of rows is a worse
 * experience than the JSON escape hatch the card also offers.
 */
export function ApprovalEditor({
  draft,
  onChange,
  invalid,
  idPrefix,
}: {
  draft: EditDraft
  onChange: (next: EditDraft) => void
  invalid: boolean
  idPrefix: string
}) {
  if (draft.tool === 'json') {
    return (
      <div>
        <Label htmlFor={`${idPrefix}-json`}>Edited action inputs (JSON)</Label>
        <Textarea
          id={`${idPrefix}-json`}
          value={draft.text}
          onChange={(event) => onChange({ ...draft, text: event.target.value })}
          className="mt-1 min-h-36 font-mono text-xs"
          aria-invalid={invalid}
        />
      </div>
    )
  }

  if (draft.tool === 'inroad_campaign_control') {
    return (
      <div className="grid gap-3">
        <div>
          <Label htmlFor={`${idPrefix}-method`}>Action</Label>
          <Select
            id={`${idPrefix}-method`}
            className="mt-1"
            value={draft.method}
            aria-invalid={invalid}
            onChange={(event) => onChange({ ...draft, method: event.target.value })}
          >
            {campaignMethods.map((method) => (
              <option key={method} value={method}>
                {method === 'pause' ? 'Pause sending' : 'Resume sending'}
              </option>
            ))}
          </Select>
        </div>
        <div>
          <Label htmlFor={`${idPrefix}-campaign`}>Campaign id</Label>
          <Input
            id={`${idPrefix}-campaign`}
            className="mt-1 font-mono text-xs"
            value={draft.campaignId}
            aria-invalid={invalid}
            onChange={(event) => onChange({ ...draft, campaignId: event.target.value })}
          />
        </div>
      </div>
    )
  }

  return (
    <div className="grid gap-3">
      <div>
        <Label htmlFor={`${idPrefix}-list`}>Target list id</Label>
        <Input
          id={`${idPrefix}-list`}
          className="mt-1 font-mono text-xs"
          value={draft.listId}
          aria-invalid={invalid}
          onChange={(event) => onChange({ ...draft, listId: event.target.value })}
        />
      </div>
      <div>
        <p className="text-[11px] text-muted-foreground">
          {draft.contacts.length} {draft.contacts.length === 1 ? 'row' : 'rows'} will be imported. Correct an
          address or drop a row; use “Edit as JSON” to change other columns.
        </p>
        <ul className="mt-1 max-h-56 space-y-1 overflow-auto rounded-md border border-border bg-background p-2">
          {draft.contacts.map((row, index) => (
            <li key={row.key} className="flex items-center gap-1.5">
              <Input
                className="h-7 min-w-0 flex-1 font-mono text-[11px]"
                value={row.email}
                aria-label={`Email for row ${index + 1}`}
                onChange={(event) => {
                  const email = event.target.value
                  const contacts = draft.contacts.slice()
                  const current = contacts[index]
                  if (!current) return
                  contacts[index] = Object.assign({}, current, { email })
                  onChange({ ...draft, contacts })
                }}
              />
              <button
                type="button"
                className="shrink-0 rounded p-1 text-faint hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={`Remove row ${index + 1}`}
                onClick={() =>
                  onChange({
                    ...draft,
                    contacts: draft.contacts.filter((_, position) => position !== index),
                  })
                }
              >
                <Trash2 className="size-3" aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

/** Before/after for every argument the reviewer changed. Empty when nothing changed. */
export function ApprovalDiff({ changes, className }: { changes: ArgumentChange[]; className?: string }) {
  if (changes.length === 0) {
    return (
      <p className={cn('text-[11px] text-muted-foreground', className)}>
        No changes yet — approving now runs the original inputs.
      </p>
    )
  }
  return (
    <div className={cn('rounded-md border border-border bg-background', className)}>
      <p className="border-b border-border px-3 py-1.5 text-[11px] font-medium text-foreground">
        {changes.length} {changes.length === 1 ? 'change' : 'changes'} from what the assistant proposed
      </p>
      <ul>
        {changes.map((change) => (
          <li key={change.key} className="flex flex-wrap gap-2 border-t border-border px-3 py-2 first:border-t-0">
            <span className="w-28 shrink-0 font-mono text-[10px] text-muted-foreground">{change.key}</span>
            <span className="min-w-0 break-words text-[11px] text-danger line-through">{change.before}</span>
            <span aria-hidden="true" className="text-[11px] text-faint">to</span>
            <span className="min-w-0 break-words text-[11px] font-medium text-ok">{change.after}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
