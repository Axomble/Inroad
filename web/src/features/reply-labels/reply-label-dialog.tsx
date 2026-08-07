import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useCreateReplyLabelMutation, useUpdateReplyLabelMutation, type ReplyLabel, type ReplyLabelInput } from './api'
import { LABEL_FLAGS } from './label-flags'
import { replyLabelErrorMessage } from './error-messages'

// Mirrors the API's ReplyLabelInput constraints exactly (hex color pattern,
// 1–80 char label) plus the two flag combinations the server refuses, so the
// form explains the rule instead of bouncing a 422 back at the user.
const replyLabelSchema = z
  .object({
    label: z.string().trim().min(1, 'Name the label').max(80, 'Keep the name to 80 characters'),
    color: z.string().regex(/^#[0-9A-Fa-f]{6}$/, 'Use a 6-digit hex color like #2563EB'),
    stopsEnrollment: z.boolean(),
    isAutomated: z.boolean(),
    suppressesContact: z.boolean(),
    capturesDeal: z.boolean(),
    defersEnrollment: z.boolean(),
  })
  .superRefine((v, ctx) => {
    if (v.isAutomated && v.stopsEnrollment) {
      ctx.addIssue({
        code: 'custom',
        path: ['stopsEnrollment'],
        message: 'An automated label cannot also stop the sequence — automated mail is not a human reply.',
      })
    }
    if (v.defersEnrollment && !v.isAutomated) {
      ctx.addIssue({
        code: 'custom',
        path: ['defersEnrollment'],
        message: 'Only automated labels can defer the sequence — mark it as automated mail first.',
      })
    }
  })

type LabelValues = z.infer<typeof replyLabelSchema>

const FLAG_FIELDS: Record<(typeof LABEL_FLAGS)[number]['field'], keyof LabelValues> = {
  stops_enrollment: 'stopsEnrollment',
  is_automated: 'isAutomated',
  suppresses_contact: 'suppressesContact',
  captures_deal: 'capturesDeal',
  defers_enrollment: 'defersEnrollment',
}

function toInput(values: LabelValues): ReplyLabelInput {
  return {
    label: values.label.trim(),
    color: values.color,
    stops_enrollment: values.stopsEnrollment,
    is_automated: values.isAutomated,
    suppresses_contact: values.suppressesContact,
    captures_deal: values.capturesDeal,
    defers_enrollment: values.defersEnrollment,
  }
}

/**
 * Create/edit form for a reply label. `initial` present means edit — the
 * immutable `key` is shown read-only there (historical enrollments store it as
 * free text, so it can never change; the server rejects attempts anyway).
 */
export function ReplyLabelDialog({
  initial,
  onClose,
  onNotice,
}: {
  initial?: ReplyLabel
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  const nameId = useId()
  const colorId = useId()
  const [create, createState] = useCreateReplyLabelMutation()
  const [update, updateState] = useUpdateReplyLabelMutation()
  const isSaving = createState.isLoading || updateState.isLoading

  const {
    register,
    handleSubmit,
    watch,
    setValue,
    setError,
    clearErrors,
    formState: { errors },
  } = useForm<LabelValues>({
    resolver: zodResolver(replyLabelSchema),
    defaultValues: initial
      ? {
          label: initial.label,
          color: initial.color,
          stopsEnrollment: initial.stops_enrollment,
          isAutomated: initial.is_automated,
          suppressesContact: initial.suppresses_contact,
          capturesDeal: initial.captures_deal,
          defersEnrollment: initial.defers_enrollment,
        }
      : {
          label: '',
          color: '#2563EB',
          stopsEnrollment: false,
          isAutomated: false,
          suppressesContact: false,
          capturesDeal: false,
          defersEnrollment: false,
        },
  })

  const color = watch('color')

  const submit = handleSubmit(async (values) => {
    clearErrors('root')
    const result = initial
      ? await update({ id: initial.id, replyLabelInput: toInput(values) })
      : await create({ replyLabelInput: toInput(values) })
    if ('error' in result) {
      setError('root', { message: replyLabelErrorMessage(initial ? 'update' : 'create', result.error) })
      return
    }
    onNotice({
      tone: 'ok',
      text: initial ? `Label “${values.label.trim()}” was updated.` : `Label “${values.label.trim()}” was created.`,
    })
    onClose()
  })

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isSaving && onClose()}>
      <AlertDialogContent className="max-h-[85vh] overflow-y-auto">
        <AlertDialogHeader>
          <AlertDialogTitle>{initial ? `Edit “${initial.label}”` : 'New reply label'}</AlertDialogTitle>
          <AlertDialogDescription>
            {initial
              ? 'Rename, recolor, or change what this label does to an enrollment when a reply receives it.'
              : 'Labels classify replies; their flags decide what happens to the enrollment that got the reply.'}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
            <div>
              <Label htmlFor={nameId}>Name</Label>
              <Input
                id={nameId}
                className="mt-1.5"
                autoFocus
                placeholder="e.g. Wants a demo"
                aria-invalid={!!errors.label}
                {...register('label')}
              />
              {errors.label && (
                <p role="alert" className="mt-1 text-xs text-danger">
                  {errors.label.message}
                </p>
              )}
            </div>
            <div>
              <Label htmlFor={colorId}>Color</Label>
              <div className="mt-1.5 flex items-center gap-2">
                {/* The native picker always yields #rrggbb; the text input is the
                    registered field so a hand-typed value still validates. */}
                <input
                  type="color"
                  aria-label="Pick a color"
                  className="size-9 shrink-0 cursor-pointer rounded-md border border-border bg-surface p-1"
                  value={/^#[0-9A-Fa-f]{6}$/.test(color) ? color : '#2563EB'}
                  onChange={(e) => setValue('color', e.target.value.toUpperCase(), { shouldValidate: true })}
                />
                <Input
                  className="w-28 font-mono"
                  aria-invalid={!!errors.color}
                  {...register('color')}
                />
              </div>
              {errors.color && (
                <p role="alert" className="mt-1 text-xs text-danger">
                  {errors.color.message}
                </p>
              )}
            </div>
          </div>

          {initial && (
            <div>
              <span className="text-[13px] font-medium text-foreground">Key</span>
              <p className="mt-1 text-[12px] text-muted-foreground">
                <code className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px]">{initial.key}</code>
                <span className="ml-2">
                  Permanent — recorded classifications reference it, so it never changes.
                </span>
              </p>
            </div>
          )}

          <fieldset>
            <legend className="text-[13px] font-medium text-foreground">Automation</legend>
            <div className="mt-2 flex flex-col gap-2">
              {LABEL_FLAGS.map((flag) => {
                const field = FLAG_FIELDS[flag.field]
                const fieldError = errors[field]
                return (
                  <div key={flag.field}>
                    <label className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground">
                      <input
                        type="checkbox"
                        className="mt-0.5 size-4 accent-primary"
                        aria-invalid={!!fieldError}
                        {...register(field)}
                      />
                      <span>
                        {flag.title}
                        <span className="block text-[11px] text-muted-foreground">{flag.description}</span>
                      </span>
                    </label>
                    {fieldError && (
                      <p role="alert" className="ml-6 mt-1 text-xs text-danger">
                        {fieldError.message}
                      </p>
                    )}
                  </div>
                )
              })}
            </div>
          </fieldset>

          {errors.root?.message && (
            <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
              {errors.root.message}
            </p>
          )}

          <AlertDialogFooter>
            <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={isSaving}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" size="sm" disabled={isSaving}>
              {isSaving && <Loader2 className="size-3.5 animate-spin" />}
              {initial ? 'Save label' : 'Create label'}
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}
