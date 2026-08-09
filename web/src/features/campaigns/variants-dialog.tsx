import { useState } from 'react'
import { Loader2, Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import { NoticeBanner, type Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  useListStepVariantsQuery,
  useCreateStepVariantMutation,
  useUpdateStepVariantMutation,
  useDeleteStepVariantMutation,
  useSetStepBaseWeightMutation,
  type StepVariant,
} from './api'
import type { StepWithId } from './step-card'
import { variantErrorMessage, splitShares } from './variant-error'

/** The base copy's key in the split map — the step's own content is variant A. */
const BASE_KEY = 'base'

/**
 * A/B variants for one step.
 *
 * The step's own subject and body are variant A and are edited in the step form,
 * not here: this dialog owns the ALTERNATIVES plus the split between them. That
 * asymmetry is the data model (the step row holds the base copy, and
 * `sends.variant_id` is NULL for it), and hiding it behind a fake symmetric
 * editor would mean this dialog silently rewriting the step every time someone
 * touched "variant A".
 *
 * Weights are relative, not percentages, so the derived share is shown beside
 * each one — an operator entering 3 and 1 wants to read 75% / 25%.
 */
export function VariantsDialog({
  campaignID,
  step,
  position,
  onClose,
}: {
  campaignID: string
  step: StepWithId
  position: number
  onClose: () => void
}) {
  const { data, isLoading, isError, refetch } = useListStepVariantsQuery({ id: campaignID, stepId: step.id })
  const [createVariant, { isLoading: creating }] = useCreateStepVariantMutation()
  const [setBaseWeight, { isLoading: settingBase }] = useSetStepBaseWeightMutation()
  const [notice, setNotice] = useState<Notice | null>(null)

  const variants = data ?? []
  const weights: Record<string, number> = { [BASE_KEY]: step.variant_weight ?? 0 }
  for (const v of variants) weights[v.id] = v.weight
  // A step with no alternatives sends its own copy whatever the weight says —
  // exactly as the send path treats it — so the split is only meaningful once
  // there is something to split against.
  const shares = variants.length === 0 ? { [BASE_KEY]: 100 } : splitShares(weights)
  const nothingSends = Object.keys(shares).length === 0

  async function onAdd() {
    const label = nextLabel(variants)
    const result = await createVariant({
      id: campaignID,
      stepId: step.id,
      stepVariantRequest: {
        label,
        weight: 1,
        // Seeded from the step's own copy rather than blank: an A/B test is a
        // change to one thing, and starting from an empty body invites shipping
        // an empty email to half the audience.
        subject: step.subject ?? '',
        body_text: step.body_text ?? '',
        body_html: step.body_html ?? '',
      },
    })
    if ('error' in result) {
      setNotice({ tone: 'error', text: variantErrorMessage('create', result.error) })
      return
    }
    setNotice({ tone: 'ok', text: `Variant ${label} added, starting at an even split.` })
  }

  async function onBaseWeight(weight: number) {
    const result = await setBaseWeight({
      id: campaignID,
      stepId: step.id,
      stepBaseWeightRequest: { weight },
    })
    if ('error' in result) {
      setNotice({ tone: 'error', text: variantErrorMessage('baseWeight', result.error) })
    }
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent className="max-w-2xl">
        <AlertDialogHeader>
          <AlertDialogTitle>A/B variants — step {position}</AlertDialogTitle>
          <AlertDialogDescription>
            Each contact gets one variant, chosen once and kept if the send is retried. Weights are relative: 3 and 1
            is a 75/25 split.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="max-h-[60vh] space-y-4 overflow-y-auto">
          {notice && <NoticeBanner notice={notice} />}
          {nothingSends && (
            <NoticeBanner
              notice={{
                tone: 'error',
                text: 'Every variant is at weight 0, so this step can’t send anything. Give one a weight above zero.',
              }}
            />
          )}

          {isLoading ? (
            <LoadingArms />
          ) : isError ? (
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">Couldn’t load this step’s variants.</p>
              <Button variant="outline" size="sm" onClick={() => void refetch()}>
                Retry
              </Button>
            </div>
          ) : (
            <>
              <BaseArm
                step={step}
                share={shares[BASE_KEY] ?? 0}
                hasVariants={variants.length > 0}
                busy={settingBase}
                onWeight={(w) => void onBaseWeight(w)}
              />
              {variants.map((variant) => (
                <VariantArm
                  key={variant.id}
                  campaignID={campaignID}
                  stepID={step.id}
                  variant={variant}
                  share={shares[variant.id] ?? 0}
                  onNotice={setNotice}
                />
              ))}
            </>
          )}
        </div>

        <AlertDialogFooter>
          <Button variant="outline" size="sm" disabled={creating || isLoading || isError} onClick={() => void onAdd()}>
            {creating ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-4" />}
            Add variant
          </Button>
          <Button variant="primary" size="sm" onClick={onClose}>
            Done
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * The step's own copy. Its subject and body are read-only here and edited in
 * the step form — this row exists so the split is visible in one place, and so
 * the base can be retired (weight 0) when a variant wins.
 */
function BaseArm({
  step,
  share,
  hasVariants,
  busy,
  onWeight,
}: {
  step: StepWithId
  share: number
  hasVariants: boolean
  busy: boolean
  onWeight: (weight: number) => void
}) {
  return (
    <section className="space-y-2 rounded-md border border-border p-3">
      <header className="flex items-center gap-2">
        <span className="text-sm font-medium">A · the step’s own copy</span>
        <span className="text-xs text-muted-foreground">{share}% of sends</span>
      </header>
      <p className="truncate text-xs text-muted-foreground">{step.subject || 'No subject (threads onto the previous email)'}</p>
      {hasVariants ? (
        <div className="flex items-center gap-2">
          <Label htmlFor="base-weight" className="text-xs">
            Weight
          </Label>
          <Input
            id="base-weight"
            type="number"
            min={0}
            className="w-24"
            defaultValue={step.variant_weight ?? 0}
            disabled={busy}
            onBlur={(e) => {
              const next = Number(e.target.value)
              if (Number.isFinite(next) && next >= 0 && next !== (step.variant_weight ?? 0)) onWeight(next)
            }}
          />
          <span className="text-xs text-muted-foreground">0 retires this copy without deleting it.</span>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          Add a variant to start splitting. Until then this copy goes to everyone.
        </p>
      )}
    </section>
  )
}

/** One alternative: label, weight, and its own subject/body. */
function VariantArm({
  campaignID,
  stepID,
  variant,
  share,
  onNotice,
}: {
  campaignID: string
  stepID: string
  variant: StepVariant
  share: number
  onNotice: (n: Notice) => void
}) {
  const [updateVariant, { isLoading: saving }] = useUpdateStepVariantMutation()
  const [deleteVariant, { isLoading: deleting }] = useDeleteStepVariantMutation()
  const [draft, setDraft] = useState({
    label: variant.label,
    weight: variant.weight,
    subject: variant.subject,
    body_text: variant.body_text,
    body_html: variant.body_html,
  })

  async function onSave() {
    const result = await updateVariant({ id: campaignID, stepId: stepID, variantId: variant.id, stepVariantRequest: draft })
    if ('error' in result) {
      onNotice({ tone: 'error', text: variantErrorMessage('update', result.error) })
      return
    }
    onNotice({ tone: 'ok', text: `Variant ${draft.label} saved.` })
  }

  async function onDelete() {
    const result = await deleteVariant({ id: campaignID, stepId: stepID, variantId: variant.id })
    if ('error' in result) {
      onNotice({ tone: 'error', text: variantErrorMessage('delete', result.error) })
      return
    }
    onNotice({ tone: 'ok', text: `Variant ${variant.label} deleted.` })
  }

  return (
    <section className="space-y-2 rounded-md border border-border p-3">
      <header className="flex items-center gap-2">
        <Input
          aria-label={`Variant ${variant.label} label`}
          className="w-28"
          value={draft.label}
          onChange={(e) => setDraft({ ...draft, label: e.target.value })}
        />
        <Input
          aria-label={`Variant ${variant.label} weight`}
          type="number"
          min={0}
          className="w-20"
          value={draft.weight}
          onChange={(e) => setDraft({ ...draft, weight: Number(e.target.value) })}
        />
        <span className="text-xs text-muted-foreground">{share}% of sends</span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Delete variant ${variant.label}`}
          className="ml-auto text-muted-foreground hover:text-danger"
          disabled={deleting}
          onClick={() => void onDelete()}
        >
          <Trash2 className="size-4" />
        </Button>
      </header>

      <Input
        aria-label={`Variant ${variant.label} subject`}
        placeholder="Subject (leave empty to thread onto the previous email)"
        value={draft.subject}
        onChange={(e) => setDraft({ ...draft, subject: e.target.value })}
      />
      <Textarea
        aria-label={`Variant ${variant.label} body`}
        rows={4}
        value={draft.body_text}
        onChange={(e) => setDraft({ ...draft, body_text: e.target.value })}
      />
      <Button variant="outline" size="sm" disabled={saving} onClick={() => void onSave()}>
        {saving && <Loader2 className="size-3.5 animate-spin" />}
        Save variant
      </Button>
    </section>
  )
}

/**
 * The next free label in the A, B, C… sequence. The base copy is A, so the first
 * alternative is B. Skips labels already taken rather than counting, because a
 * deleted middle variant would otherwise produce a duplicate the API rejects.
 */
function nextLabel(variants: StepVariant[]): string {
  const taken = new Set(variants.map((v) => v.label))
  for (let i = 1; i < 26; i++) {
    const label = String.fromCharCode(65 + i)
    if (!taken.has(label)) return label
  }
  return `V${variants.length + 1}`
}

function LoadingArms() {
  return (
    <div className="space-y-3">
      {[0, 1].map((i) => (
        <div key={i} className="space-y-2 rounded-md border border-border p-3">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-9 w-full" />
        </div>
      ))}
    </div>
  )
}
