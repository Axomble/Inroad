import { useId, useState } from 'react'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
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
import { httpStatus } from '@/lib/rtk-error'
import { useCreateAiProviderMutation, useUpdateAiProviderMutation, type AiProvider } from './api'
import { PROVIDER_KIND_GROUPS, PROVIDER_KINDS, KIND_META, type ProviderKindMeta } from './provider-kinds'
import { configValue, fieldValid, providerTitle } from './provider-format'

/**
 * "Add provider" — a kind picker (Direct / Via your cloud / Gateway), then the
 * kind-specific connect form. Same model, whichever door the workspace has.
 */
export function AddProviderDialog({
  onClose,
  onNotice,
  onConnected,
}: {
  onClose: () => void
  onNotice: (n: Notice) => void
  /** Fires with the created row so the caller can chain (e.g. discovery). */
  onConnected: (provider: AiProvider) => void
}) {
  const [meta, setMeta] = useState<ProviderKindMeta | null>(null)

  return (
    <AlertDialog open onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        {meta === null ? (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Add an AI provider</AlertDialogTitle>
              <AlertDialogDescription>
                A direct API key is the 30-second path; your cloud account or a gateway works just as well.
              </AlertDialogDescription>
            </AlertDialogHeader>

            <div className="flex flex-col gap-4">
              {PROVIDER_KIND_GROUPS.map((group) => (
                <div key={group.id}>
                  <p className="mb-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">{group.label}</p>
                  <div className="grid gap-2 sm:grid-cols-2">
                    {PROVIDER_KINDS.filter((k) => k.group === group.id).map((kind) => {
                      const Icon = kind.icon
                      return (
                        <button
                          key={kind.kind}
                          type="button"
                          onClick={() => setMeta(kind)}
                          className="flex items-start gap-2.5 rounded-md border border-border bg-surface p-3 text-left transition-colors hover:border-border-strong hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                        >
                          <Icon className="mt-0.5 size-4 shrink-0 text-foreground" />
                          <span className="min-w-0">
                            <span className="block text-[13px] font-medium text-foreground">{kind.title}</span>
                            <span className="mt-0.5 block text-[11.5px] leading-snug text-muted-foreground">
                              {kind.blurb}
                            </span>
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>

            <AlertDialogFooter>
              <Button variant="ghost" size="sm" onClick={onClose}>
                Cancel
              </Button>
            </AlertDialogFooter>
          </>
        ) : (
          <ProviderConnectForm
            meta={meta}
            onBack={() => setMeta(null)}
            onClose={onClose}
            onNotice={onNotice}
            onConnected={onConnected}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** Edit an existing row: config/name editable; blank secrets keep the stored ones. */
export function EditProviderDialog({
  provider,
  onClose,
  onNotice,
}: {
  provider: AiProvider
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  return (
    <AlertDialog open onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <ProviderConnectForm
          meta={KIND_META[provider.kind]}
          existing={provider}
          onClose={onClose}
          onNotice={onNotice}
        />
      </AlertDialogContent>
    </AlertDialog>
  )
}

function ProviderConnectForm({
  meta,
  existing,
  onBack,
  onClose,
  onNotice,
  onConnected,
}: {
  meta: ProviderKindMeta
  existing?: AiProvider
  /** Back to the kind picker — only when adding. */
  onBack?: () => void
  onClose: () => void
  onNotice: (n: Notice) => void
  onConnected?: (provider: AiProvider) => void
}) {
  const formId = useId()
  const [create, createState] = useCreateAiProviderMutation()
  const [update, updateState] = useUpdateAiProviderMutation()
  const isLoading = createState.isLoading || updateState.isLoading

  const [displayName, setDisplayName] = useState(existing?.display_name ?? '')
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(meta.fields.filter((f) => !f.secret).map((f) => [f.key, configValue(existing?.config, f.key)])),
  )
  const [error, setError] = useState<string | null>(null)

  // Editing never demands re-typing secrets; blank means keep what's stored.
  const requireSecrets = !existing
  const canSave = meta.fields.every((f) => fieldValid(f, values[f.key], requireSecrets)) && !isLoading

  function setValue(key: string, value: string) {
    setValues((prev) => ({ ...prev, [key]: value }))
    setError(null)
  }

  async function onSave() {
    if (!canSave) return
    setError(null)

    const credentials: Record<string, string> = {}
    const config: Record<string, string> = {}
    for (const field of meta.fields) {
      const value = values[field.key]?.trim()
      if (!value) continue
      ;(field.secret ? credentials : config)[field.key] = value
    }

    const result = existing
      ? await update({
          id: existing.id,
          aiProviderUpdateRequest: {
            display_name: displayName.trim(),
            config,
            // Only send credentials when something was actually re-entered.
            ...(Object.keys(credentials).length > 0 ? { credentials } : {}),
          },
        })
      : await create({
          aiProviderCreateRequest: {
            kind: meta.kind,
            ...(displayName.trim() ? { display_name: displayName.trim() } : {}),
            credentials,
            config,
          },
        })

    if ('error' in result) {
      const status = httpStatus(result.error)
      setError(
        status === 409
          ? 'This provider is already connected — edit the existing entry instead.'
          : status === 400
            ? 'The provider rejected these details — double-check them and try again.'
            : "Couldn't save the provider. Please try again.",
      )
      return
    }
    onNotice({
      tone: 'ok',
      text: existing ? `${providerTitle(existing)} updated.` : `${meta.title} connected.`,
    })
    onClose()
    if (!existing) onConnected?.(result.data)
  }

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="flex items-center gap-2">
          {onBack && (
            <button
              type="button"
              onClick={onBack}
              aria-label="Back to provider list"
              className="grid size-6 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-surface-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              <ArrowLeft className="size-4" />
            </button>
          )}
          {existing ? `Edit ${providerTitle(existing)}` : `Connect ${meta.title}`}
        </AlertDialogTitle>
        <AlertDialogDescription>{meta.helper ?? meta.blurb}</AlertDialogDescription>
      </AlertDialogHeader>

      <div className="flex flex-col gap-4">
        <div>
          <Label htmlFor={`${formId}-name`}>Display name (optional)</Label>
          <Input
            id={`${formId}-name`}
            className="mt-1.5"
            placeholder={`e.g. ${meta.title}`}
            value={displayName}
            onChange={(e) => {
              setDisplayName(e.target.value)
              setError(null)
            }}
          />
        </div>

        {meta.fields.map((field) => {
          const id = `${formId}-${field.key}`
          const value = values[field.key] ?? ''
          const invalid = value.trim().length > 0 && !fieldValid(field, value, requireSecrets)
          const label =
            existing && field.secret && field.required ? `${field.label} (leave blank to keep)` : field.label
          return (
            <div key={field.key}>
              <Label htmlFor={id}>{label}</Label>
              <div className="mt-1.5">
                {field.input === 'password' ? (
                  <PasswordInput
                    id={id}
                    placeholder={field.placeholder}
                    autoComplete="off"
                    value={value}
                    onChange={(e) => setValue(field.key, e.target.value)}
                  />
                ) : field.input === 'json' ? (
                  <Textarea
                    id={id}
                    className="min-h-24 font-mono text-[12px]"
                    placeholder={field.placeholder}
                    value={value}
                    aria-invalid={invalid}
                    onChange={(e) => setValue(field.key, e.target.value)}
                  />
                ) : (
                  <Input
                    id={id}
                    type={field.input === 'url' ? 'url' : 'text'}
                    placeholder={field.placeholder}
                    autoComplete="off"
                    value={value}
                    aria-invalid={invalid}
                    onChange={(e) => setValue(field.key, e.target.value)}
                  />
                )}
              </div>
              {field.helper && <p className="mt-1 text-[11px] text-muted-foreground">{field.helper}</p>}
            </div>
          )
        })}

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
          {existing ? 'Save changes' : `Connect ${meta.title}`}
        </Button>
      </AlertDialogFooter>
    </>
  )
}
