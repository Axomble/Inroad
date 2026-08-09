import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  useCreateCustomFieldMutation,
  useUpdateCustomFieldMutation,
  type CustomFieldDef,
  type CustomFieldType,
} from './api'
import { customFieldErrorMessage } from './custom-field-error-messages'

/**
 * The key pattern is the API's, character for character (`^[a-z][a-z0-9_]{0,39}$`).
 * Restating it here is not duplication for its own sake: it is what lets the form
 * explain the rule as you type instead of bouncing a 400 back after a round trip.
 * The server remains the authority — this can only ever be stricter-or-equal, and
 * an accepted value is still validated there.
 */
const KEY_PATTERN = /^[a-z][a-z0-9_]{0,39}$/

const FIELD_TYPES: { value: CustomFieldType; label: string; hint: string }[] = [
  { value: 'text', label: 'Text', hint: 'Any text. The most common choice.' },
  { value: 'number', label: 'Number', hint: 'Digits only — rejects "about 50".' },
  { value: 'date', label: 'Date', hint: 'YYYY-MM-DD, so a date never means two different days.' },
  { value: 'select', label: 'Select', hint: 'One of a fixed list of options.' },
]

const fieldSchema = z
  .object({
    key: z
      .string()
      .trim()
      .toLowerCase()
      .regex(KEY_PATTERN, 'Start with a letter; lower-case letters, digits and underscores only (max 40)'),
    label: z.string().trim().min(1, 'Name the field').max(80, 'Keep the name to 80 characters'),
    type: z.enum(['text', 'number', 'date', 'select']),
    // One option per line is the whole editor: a chip input would be more
    // fashionable and strictly worse to paste a real list into.
    options: z.string(),
  })
  .superRefine((v, ctx) => {
    if (v.type !== 'select') return
    if (parseOptions(v.options).length === 0) {
      ctx.addIssue({ code: 'custom', path: ['options'], message: 'A select needs at least one option' })
    }
  })

type FieldValues = z.infer<typeof fieldSchema>

/** Splits the textarea into trimmed, de-duplicated, non-empty options. */
function parseOptions(raw: string): string[] {
  const seen = new Set<string>()
  for (const line of raw.split('\n')) {
    const option = line.trim()
    if (option) seen.add(option)
  }
  return [...seen]
}

/**
 * Create or edit one custom field definition.
 *
 * In EDIT mode the key and type are shown but disabled, because the API refuses
 * to change either: the key addresses values already stored on contacts and
 * appears in tokens operators have typed into live sequences, and the type is
 * the promise every existing value was validated under. Showing them greyed out
 * rather than hiding them is the point — it answers "why can't I change this?"
 * in the same glance that asks it.
 */
export function CustomFieldDialog({
  initial,
  onClose,
  onNotice,
}: {
  initial?: CustomFieldDef
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  const editing = initial !== undefined
  const formId = useId()
  const [createField, { isLoading: creating }] = useCreateCustomFieldMutation()
  const [updateField, { isLoading: updating }] = useUpdateCustomFieldMutation()
  const saving = creating || updating

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<FieldValues>({
    resolver: zodResolver(fieldSchema),
    defaultValues: {
      key: initial?.key ?? '',
      label: initial?.label ?? '',
      type: initial?.type ?? 'text',
      options: (initial?.options ?? []).join('\n'),
    },
  })
  const type = watch('type')

  async function onSubmit(values: FieldValues) {
    const options = values.type === 'select' ? parseOptions(values.options) : undefined
    const result = editing
      ? await updateField({
          fieldId: initial.id,
          customFieldUpdate: { label: values.label, options },
        })
      : await createField({
          customFieldCreate: { key: values.key, label: values.label, type: values.type, options },
        })

    // Close first so the outcome banner isn't hidden under the dialog.
    if ('error' in result) {
      onNotice({ tone: 'error', text: customFieldErrorMessage(editing ? 'update' : 'create', result.error) })
      return
    }
    onClose()
    onNotice({
      tone: 'ok',
      text: editing ? `Field “${values.label}” was saved.` : `Field “${values.label}” was created.`,
    })
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !saving && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{editing ? `Edit “${initial.label}”` : 'New custom field'}</AlertDialogTitle>
          <AlertDialogDescription>
            {editing
              ? 'The key and type are fixed once a field exists — contacts already hold values under them, and sequences already reference the key.'
              : 'Custom fields are stored per contact, mapped from CSV columns of the same name, and usable in sequences as a personalization token.'}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <form id={formId} className="space-y-4" onSubmit={(e) => void handleSubmit(onSubmit)(e)}>
          <div className="space-y-1.5">
            <Label htmlFor={`${formId}-label`}>Name</Label>
            <Input id={`${formId}-label`} placeholder="Industry" autoFocus={!editing} {...register('label')} />
            {errors.label && <FieldError message={errors.label.message} />}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor={`${formId}-key`}>Key</Label>
            <Input
              id={`${formId}-key`}
              placeholder="industry"
              disabled={editing}
              className="font-mono"
              {...register('key')}
            />
            {errors.key ? (
              <FieldError message={errors.key.message} />
            ) : (
              <p className="text-xs text-muted-foreground">
                Used as <code className="font-mono">{'{{custom.'}{watch('key') || 'key'}{'}}'}</code> in sequences,
                and as the CSV column name on import.
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor={`${formId}-type`}>Type</Label>
            <Select id={`${formId}-type`} disabled={editing} {...register('type')}>
              {FIELD_TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
            <p className="text-xs text-muted-foreground">
              {FIELD_TYPES.find((t) => t.value === type)?.hint}
            </p>
          </div>

          {type === 'select' && (
            <div className="space-y-1.5">
              <Label htmlFor={`${formId}-options`}>Options</Label>
              <Textarea id={`${formId}-options`} rows={5} placeholder={'Tier A\nTier B\nTier C'} {...register('options')} />
              {errors.options ? (
                <FieldError message={errors.options.message} />
              ) : (
                <p className="text-xs text-muted-foreground">
                  One per line. Removing an option later does not change contacts that already hold it — their value
                  keeps sending, it just stops being offered here.
                </p>
              )}
            </div>
          )}
        </form>

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" size="sm" disabled={saving}>
            {saving && <Loader2 className="size-3.5 animate-spin" />}
            {editing ? 'Save field' : 'Create field'}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return (
    <p role="alert" className="text-xs text-destructive">
      {message}
    </p>
  )
}
