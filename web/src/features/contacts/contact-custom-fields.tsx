import { useEffect, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { NoticeBanner, type Notice } from '@/components/shared/notice-banner'
import {
  useGetContactCustomFieldsQuery,
  useSetContactCustomFieldsMutation,
  type CustomFieldValue,
} from './api'
import { customFieldErrorMessage } from './custom-field-error-messages'

/** The HTML input type that matches a field's declared type. */
const INPUT_TYPE = { text: 'text', number: 'number', date: 'date' } as const

/**
 * The custom-field block on a contact's record page.
 *
 * Editing is whole-form: the PUT replaces the contact's live field set, so the
 * form must submit every live field it rendered. Saving one input at a time
 * would silently clear the others, which is exactly the failure the API's
 * replace semantics make possible if a caller submits a partial set.
 *
 * Orphaned values — keys with no live definition, from an archived field or from
 * before definitions existed — are rendered READ-ONLY beneath the form. They are
 * real data that still substitutes into emails, so hiding them would misrepresent
 * the record; but they cannot be edited, because the API refuses writes to a key
 * with no live field.
 */
export function ContactCustomFields({ contactId }: { contactId: string }) {
  const { data, isLoading, isError, refetch } = useGetContactCustomFieldsQuery({ id: contactId })
  const [saveFields, { isLoading: saving }] = useSetContactCustomFieldsMutation()
  const [notice, setNotice] = useState<Notice | null>(null)
  const [draft, setDraft] = useState<Record<string, string> | null>(null)

  const rows = data ?? []
  const editable = rows.filter((r) => r.def !== null && r.def !== undefined)
  const orphans = rows.filter((r) => r.def === null || r.def === undefined)

  // The draft is seeded from the server response and re-seeded whenever it
  // changes, so an edit elsewhere (or a definition being archived) is reflected
  // rather than overwritten by a stale form. Keying on the fetched rows rather
  // than on `isLoading` means a background refetch that changes nothing leaves
  // in-progress typing alone.
  useEffect(() => {
    if (!data) return
    setDraft(Object.fromEntries(data.map((row) => [row.key, row.value])))
  }, [data])

  if (isLoading) return <LoadingFields />
  if (isError) {
    return (
      <div className="space-y-2 px-5 py-4">
        <p className="text-sm text-muted-foreground">Couldn’t load this contact’s custom fields.</p>
        <Button variant="outline" size="sm" onClick={() => void refetch()}>
          Retry
        </Button>
      </div>
    )
  }
  if (rows.length === 0) {
    return (
      <p className="px-5 py-4 text-sm text-muted-foreground">
        This workspace has no custom fields yet. Define them under Settings → Custom fields to store data like
        industry or renewal date, and use it in sequences.
      </p>
    )
  }

  const values = draft ?? {}

  async function onSubmit() {
    // Only LIVE keys are submitted: the API refuses a write to an archived or
    // unknown key, and the values under those keys are preserved server-side
    // precisely because the form never showed them.
    const payload = Object.fromEntries(editable.map((row) => [row.key, values[row.key] ?? '']))
    const result = await saveFields({ id: contactId, customFieldValueSet: { values: payload } })
    if ('error' in result) {
      setNotice({ tone: 'error', text: customFieldErrorMessage('saveValues', result.error) })
      return
    }
    setNotice({ tone: 'ok', text: 'Custom fields saved.' })
  }

  return (
    <div className="space-y-4 px-5 py-4">
      {notice && <NoticeBanner notice={notice} />}

      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault()
          void onSubmit()
        }}
      >
        {editable.map((row) => (
          <FieldInput
            key={row.key}
            row={row}
            value={values[row.key] ?? ''}
            onChange={(next) => setDraft({ ...values, [row.key]: next })}
          />
        ))}

        {editable.length > 0 && (
          <div className="pt-1">
            <Button type="submit" variant="primary" size="sm" disabled={saving}>
              {saving && <Loader2 className="size-3.5 animate-spin" />}
              Save fields
            </Button>
          </div>
        )}
      </form>

      {orphans.length > 0 && (
        <div className="space-y-2 border-t border-border pt-3">
          <p className="text-xs text-muted-foreground">
            Stored under fields that are archived or no longer defined. These still substitute into emails, but can’t
            be edited here.
          </p>
          {orphans.map((row) => (
            <div key={row.key} className="flex items-baseline gap-2 text-sm">
              <code className="font-mono text-xs text-muted-foreground">{row.key}</code>
              <span className="truncate">{row.value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

/** One input, shaped by its field's type. A select renders its allowed options
 * plus an empty choice, because "no value" is always legal. */
function FieldInput({
  row,
  value,
  onChange,
}: {
  row: CustomFieldValue
  value: string
  onChange: (next: string) => void
}) {
  const def = row.def
  if (!def) return null
  const inputId = `custom-field-${def.key}`

  return (
    <div className="space-y-1.5">
      <Label htmlFor={inputId}>{def.label}</Label>
      {def.type === 'select' ? (
        <Select id={inputId} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">—</option>
          {def.options.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
          {/* A value stored before an option was removed would otherwise vanish
              from the select and be silently cleared on the next save. */}
          {value && !def.options.includes(value) && (
            <option value={value}>{value} (no longer an option)</option>
          )}
        </Select>
      ) : (
        <Input
          id={inputId}
          type={INPUT_TYPE[def.type]}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  )
}

function LoadingFields() {
  return (
    <div className="space-y-3 px-5 py-4">
      {[0, 1].map((i) => (
        <div key={i} className="space-y-1.5">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-9 w-full" />
        </div>
      ))}
    </div>
  )
}
