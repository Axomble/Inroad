import { useState } from 'react'
import { KeyRound, Loader2, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { relativeTime } from '@/lib/relative-time'
import {
  useDeleteAiModelMutation,
  useDeleteAiProviderMutation,
  type AiDiscoveredModel,
  type AiModel,
  type AiProvider,
} from './api'
import { KIND_META } from './provider-kinds'
import { configSummary, formatTokens, providerTitle } from './provider-format'
import { EditProviderDialog } from './provider-form'
import { AddModelDialog } from './add-model-dialog'
import { DiscoverModelsDialog } from './discover-models-dialog'

/**
 * One connected provider: identity + config chips + masked credential head,
 * Edit/Remove, and its custom-model list with the two ways in — discovery
 * ("Fetch models") and manual entry.
 */
export function ProviderRow({
  provider,
  models,
  onNotice,
}: {
  provider: AiProvider
  /** This row's custom models (already filtered by provider_id). */
  models: AiModel[]
  onNotice: (n: Notice) => void
}) {
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [discovering, setDiscovering] = useState(false)
  const [addingModel, setAddingModel] = useState<{ prefill?: AiDiscoveredModel } | null>(null)
  const [remove, { isLoading: removing }] = useDeleteAiProviderMutation()

  const meta = KIND_META[provider.kind]
  const Icon = meta.icon
  const name = providerTitle(provider)

  async function onRemove() {
    const result = await remove({ id: provider.id })
    // Close first so an error banner isn't hidden under the dialog.
    setConfirming(false)
    if ('error' in result) {
      onNotice({ tone: 'error', text: `Couldn't remove ${name}. Please try again.` })
    } else {
      onNotice({ tone: 'ok', text: `${name} removed.` })
    }
  }

  return (
    <div className="border-b border-border px-5 py-4">
      <div className="flex flex-wrap items-center gap-3">
        <Icon className="size-4 shrink-0 text-foreground" />
        <span className="text-[13.5px] font-medium text-foreground">{name}</span>
        {provider.display_name.trim() && provider.display_name !== meta.title && (
          <span className="text-[11.5px] text-faint">{meta.title}</span>
        )}
        {configSummary(provider).map((value) => (
          <code
            key={value}
            className="max-w-64 truncate rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground"
          >
            {value}
          </code>
        ))}
        {provider.key_prefix && (
          <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-faint">
            <KeyRound className="size-3.5" strokeWidth={1.75} aria-hidden="true" />
            {provider.key_prefix}…
          </span>
        )}
        <span className="font-mono text-[11px] text-faint">updated {relativeTime(provider.updated_at)}</span>

        <div className="ml-auto flex items-center gap-2">
          <Button variant="outline" size="sm" aria-label={`Edit ${name}`} onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={removing}
            aria-label={`Remove ${name}`}
            onClick={() => setConfirming(true)}
          >
            {removing ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
            Remove
          </Button>
        </div>
      </div>

      <div className="mt-3 border-l-2 border-border pl-4">
        <div className="flex flex-wrap items-center gap-1">
          <span className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">
            Models{models.length > 0 && <span className="tabular-nums"> · {models.length}</span>}
          </span>
          <div className="ml-auto flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              aria-label={`Fetch models from ${name}`}
              onClick={() => setDiscovering(true)}
            >
              <RefreshCw className="size-3.5" />
              Fetch models
            </Button>
            <Button
              variant="ghost"
              size="sm"
              aria-label={`Add model to ${name}`}
              onClick={() => setAddingModel({})}
            >
              <Plus className="size-3.5" />
              Add model
            </Button>
          </div>
        </div>
        {models.length === 0 ? (
          <p className="mt-1 text-[12px] text-muted-foreground">
            No custom models yet — fetch them from the provider, or add a model ID manually.
          </p>
        ) : (
          <div className="mt-1 flex flex-col">
            {models.map((model) => (
              <ProviderModelRow key={model.id} model={model} onNotice={onNotice} />
            ))}
          </div>
        )}
      </div>

      <AlertDialog open={confirming} onOpenChange={(next) => !next && setConfirming(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Its credentials and its {models.length === 1 ? 'model' : 'models'} are removed with it, and
              anything the assistant routes through this provider stops working immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={removing}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={removing} onClick={() => void onRemove()}>
              {removing && <Loader2 className="size-3.5 animate-spin" />}
              Remove provider
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {editing && <EditProviderDialog provider={provider} onClose={() => setEditing(false)} onNotice={onNotice} />}
      {discovering && (
        <DiscoverModelsDialog
          provider={provider}
          existingNames={models.map((m) => m.name)}
          onClose={() => setDiscovering(false)}
          onManualAdd={(prefill) => {
            setDiscovering(false)
            setAddingModel({ prefill })
          }}
          onNotice={onNotice}
        />
      )}
      {addingModel && (
        <AddModelDialog
          provider={provider}
          prefill={addingModel.prefill}
          onClose={() => setAddingModel(null)}
          onNotice={onNotice}
        />
      )}
    </div>
  )
}

function ProviderModelRow({ model, onNotice }: { model: AiModel; onNotice: (n: Notice) => void }) {
  const [confirming, setConfirming] = useState(false)
  const [remove, { isLoading }] = useDeleteAiModelMutation()

  async function onRemove() {
    // Only source=custom rows render here, and those always carry the
    // deletable row id — the guard keeps the invariant loud if that changes.
    if (!model.custom_model_id) {
      onNotice({ tone: 'error', text: `“${model.label}” is a catalog model and can't be removed.` })
      setConfirming(false)
      return
    }
    const result = await remove({ id: model.custom_model_id })
    setConfirming(false)
    if ('error' in result) {
      onNotice({ tone: 'error', text: `Couldn't remove the model “${model.label}”. Please try again.` })
    } else {
      onNotice({ tone: 'ok', text: `Model “${model.label}” removed.` })
    }
  }

  return (
    <div className="flex items-center gap-2.5 border-b border-border/60 py-1.5 last:border-b-0">
      <span className="text-[13px] text-foreground">{model.label}</span>
      <code className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] text-muted-foreground">
        {model.name}
      </code>
      <span className="font-mono text-[11px] text-faint">
        {formatTokens(model.context_window_tokens)}
        {model.supports_reasoning ? ' · reasoning' : ''}
      </span>
      <Button
        variant="ghost"
        size="sm"
        className="ml-auto"
        disabled={isLoading}
        aria-label={`Remove model ${model.label}`}
        onClick={() => setConfirming(true)}
      >
        {isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
      </Button>

      <AlertDialog open={confirming} onOpenChange={(next) => !next && setConfirming(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove the model “{model.label}”?</AlertDialogTitle>
            <AlertDialogDescription>
              It disappears from the workspace's model list and can't be picked as a default. You can add it
              back later.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={isLoading}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onRemove()}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              Remove model
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
