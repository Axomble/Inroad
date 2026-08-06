import { useId, useState } from 'react'
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
import { httpStatus } from '@/lib/rtk-error'
import { useCreateAiModelMutation, type AiDiscoveredModel, type AiProvider } from './api'
import { providerTitle } from './provider-format'

/**
 * Manual model entry — the universal fallback for providers that can't list
 * their models (and the edit step for bare-id discovery candidates, which
 * arrive as `prefill`).
 */
export function AddModelDialog({
  provider,
  prefill,
  onClose,
  onNotice,
}: {
  provider: AiProvider
  /** A discovery candidate to start from (name, maybe label/context). */
  prefill?: AiDiscoveredModel
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  const nameId = useId()
  const labelId = useId()
  const contextId = useId()
  const outputId = useId()
  const [create, { isLoading }] = useCreateAiModelMutation()

  const [name, setName] = useState(prefill?.name ?? '')
  const [label, setLabel] = useState(prefill?.label ?? '')
  const [contextTokens, setContextTokens] = useState(String(prefill?.context_window_tokens ?? 128000))
  const [outputTokens, setOutputTokens] = useState('16000')
  const [supportsReasoning, setSupportsReasoning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const contextValue = Number(contextTokens)
  const outputValue = Number(outputTokens)
  const tokensValid =
    Number.isInteger(contextValue) && contextValue > 0 && Number.isInteger(outputValue) && outputValue > 0
  const canSave = name.trim().length > 0 && label.trim().length > 0 && tokensValid && !isLoading

  async function onSave() {
    if (!canSave) return
    setError(null)

    const result = await create({
      aiModelCreateRequest: {
        provider_id: provider.id,
        name: name.trim(),
        label: label.trim(),
        context_window_tokens: contextValue,
        max_output_tokens: outputValue,
        supports_reasoning: supportsReasoning,
        // Costs ride along only when discovery reported them.
        ...(prefill?.input_cost_per_mtok != null ? { input_cost_per_mtok: prefill.input_cost_per_mtok } : {}),
        ...(prefill?.output_cost_per_mtok != null ? { output_cost_per_mtok: prefill.output_cost_per_mtok } : {}),
      },
    })

    if ('error' in result) {
      setError(
        httpStatus(result.error) === 400
          ? 'The provider rejected this model — check the name and token limits.'
          : "Couldn't add the model. Please try again.",
      )
      return
    }
    onNotice({ tone: 'ok', text: `Model “${label.trim()}” added to ${providerTitle(provider)}.` })
    onClose()
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isLoading && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Add a model to {providerTitle(provider)}</AlertDialogTitle>
          <AlertDialogDescription>
            Enter the model exactly as the provider expects it — the name is what's sent on every request.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <Label htmlFor={nameId}>Model name</Label>
              <Input
                id={nameId}
                className="mt-1.5"
                autoFocus={!prefill}
                placeholder="e.g. meta-llama/llama-4-maverick"
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setError(null)
                }}
              />
            </div>
            <div>
              <Label htmlFor={labelId}>Label</Label>
              <Input
                id={labelId}
                className="mt-1.5"
                autoFocus={Boolean(prefill)}
                placeholder="e.g. Llama 4 Maverick"
                value={label}
                onChange={(e) => {
                  setLabel(e.target.value)
                  setError(null)
                }}
              />
            </div>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <Label htmlFor={contextId}>Context window (tokens)</Label>
              <Input
                id={contextId}
                type="number"
                min={1}
                inputMode="numeric"
                className="mt-1.5"
                value={contextTokens}
                onChange={(e) => {
                  setContextTokens(e.target.value)
                  setError(null)
                }}
              />
            </div>
            <div>
              <Label htmlFor={outputId}>Max output (tokens)</Label>
              <Input
                id={outputId}
                type="number"
                min={1}
                inputMode="numeric"
                className="mt-1.5"
                value={outputTokens}
                onChange={(e) => {
                  setOutputTokens(e.target.value)
                  setError(null)
                }}
              />
            </div>
          </div>

          <label className="flex cursor-pointer items-center gap-2 text-[13px] text-foreground">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={supportsReasoning}
              onChange={(e) => setSupportsReasoning(e.target.checked)}
            />
            Supports extended reasoning
          </label>

          {error && (
            <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
              {error}
            </p>
          )}
        </div>

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" disabled={!canSave} onClick={() => void onSave()}>
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Add model
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
