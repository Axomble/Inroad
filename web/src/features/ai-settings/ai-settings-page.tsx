import { useId, useState } from 'react'
import { Cable, Cloud, KeyRound, Loader2, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { NoticeBanner, type Notice } from '@/components/shared/notice-banner'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { useHasRole } from '@/hooks/use-has-role'
import { httpStatus } from '@/lib/rtk-error'
import { ProviderRow } from './provider-row'
import { AddProviderDialog } from './provider-form'
import { AddModelDialog } from './add-model-dialog'
import { DiscoverModelsDialog } from './discover-models-dialog'
import { formatTokens, providerTitle } from './provider-format'
import {
  DEFAULT_FAST_MODEL,
  DEFAULT_SMART_MODEL,
  useGetAiSettingsQuery,
  useListAiModelsQuery,
  useListAiProvidersQuery,
  useUpdateAiSettingsMutation,
  type AiDiscoveredModel,
  type AiModel,
  type AiProvider,
} from './api'
import type { AiSettings } from './api'

/**
 * Settings → AI (agent platform PR A1). Admin-gated like the API-keys panel:
 * the server enforces the role; this mirrors it so non-admins see an honest
 * empty state instead of a dead form. Providers and custom models mutate
 * immediately (they are secrets / catalog rows — no draft state), while
 * models/defaults/instructions edit locally and persist together through one
 * PUT /ai/settings.
 */
export function AiSettingsPage() {
  const isAdmin = useHasRole('admin')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [adding, setAdding] = useState(false)
  // Post-connect chain: a fresh gateway goes straight to model discovery.
  const [discoverFor, setDiscoverFor] = useState<AiProvider | null>(null)
  const [manualAddFor, setManualAddFor] = useState<{ provider: AiProvider; prefill?: AiDiscoveredModel } | null>(null)

  const providersQuery = useListAiProvidersQuery(undefined, { skip: !isAdmin })
  const modelsQuery = useListAiModelsQuery(undefined, { skip: !isAdmin })
  const settingsQuery = useGetAiSettingsQuery(undefined, { skip: !isAdmin })

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Workspace" title="AI" />
        <EmptyBlock
          title="Admins only"
          description="Ask a workspace owner or admin to configure AI providers and models."
        />
      </Page>
    )
  }

  const isLoading = providersQuery.isLoading || modelsQuery.isLoading || settingsQuery.isLoading
  const isError = providersQuery.isError || modelsQuery.isError || settingsQuery.isError

  const providers = providersQuery.data?.providers ?? []
  const models = modelsQuery.data?.models ?? []
  const customModels = models.filter((m) => m.source === 'custom')

  return (
    <Page>
      <PageTopbar
        eyebrow="Workspace"
        title="AI"
        subtitle="Providers, enabled models, and workspace defaults for the assistant"
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        {isLoading ? (
          <LoadingSections />
        ) : isError ? (
          <EmptyBlock
            title="Couldn't load AI settings"
            description="Something went wrong fetching this workspace's AI configuration. Please try again."
            action={
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  void providersQuery.refetch()
                  void modelsQuery.refetch()
                  void settingsQuery.refetch()
                }}
              >
                Retry
              </Button>
            }
          />
        ) : (
          <>
            <SectionBar label="Providers" count={providers.length > 0 ? providers.length : undefined}>
              <Button variant="outline" size="sm" onClick={() => setAdding(true)}>
                <Plus className="size-3.5" />
                Add provider
              </Button>
            </SectionBar>

            {providers.length === 0 ? (
              <ProvidersZeroState onAdd={() => setAdding(true)} />
            ) : (
              providers.map((provider) => (
                <ProviderRow
                  key={provider.id}
                  provider={provider}
                  models={customModels.filter((m) => m.provider_id === provider.id)}
                  onNotice={setNotice}
                />
              ))
            )}

            {settingsQuery.data && modelsQuery.data && (
              <PreferencesForm
                // Remount when a save lands so the form re-seeds from the
                // server truth (and "unsaved changes" resets).
                key={JSON.stringify(settingsQuery.data)}
                settings={settingsQuery.data}
                models={models}
                providers={providers}
                onNotice={setNotice}
              />
            )}
          </>
        )}
      </PageBody>

      {adding && (
        <AddProviderDialog
          onClose={() => setAdding(false)}
          onNotice={setNotice}
          onConnected={(provider) => {
            // Gateways go straight to discovery; other kinds surface models
            // from the catalog (or via the row's own Fetch models button).
            if (provider.kind === 'openai_compatible') setDiscoverFor(provider)
          }}
        />
      )}
      {discoverFor && (
        <DiscoverModelsDialog
          provider={discoverFor}
          existingNames={customModels.filter((m) => m.provider_id === discoverFor.id).map((m) => m.name)}
          onClose={() => setDiscoverFor(null)}
          onManualAdd={(prefill) => {
            setManualAddFor({ provider: discoverFor, prefill })
            setDiscoverFor(null)
          }}
          onNotice={setNotice}
        />
      )}
      {manualAddFor && (
        <AddModelDialog
          provider={manualAddFor.provider}
          prefill={manualAddFor.prefill}
          onClose={() => setManualAddFor(null)}
          onNotice={setNotice}
        />
      )}
    </Page>
  )
}

/**
 * The welcome mat for a workspace with no providers at all: name the three
 * doors in, each opening the same Add-provider flow.
 */
function ProvidersZeroState({ onAdd }: { onAdd: () => void }) {
  const doors = [
    {
      icon: KeyRound,
      title: 'A direct API key',
      copy: 'Anthropic, OpenAI, or Google — paste a key and you’re done.',
    },
    {
      icon: Cloud,
      title: 'Through your cloud',
      copy: 'Azure OpenAI, AWS Bedrock, or Vertex AI, with the credentials you already manage.',
    },
    {
      icon: Cable,
      title: 'A gateway',
      copy: 'OpenRouter, LiteLLM, Ollama, or any OpenAI-compatible endpoint.',
    },
  ]
  return (
    <div className="border-b border-border px-5 py-10">
      <div className="mx-auto max-w-2xl text-center">
        <p className="text-sm font-medium text-foreground">Connect a provider to turn on the assistant</p>
        <p className="mx-auto mt-1.5 max-w-md text-sm text-muted-foreground">
          Bring whichever access you already have — one is enough to start, and you can mix them freely.
        </p>
        <div className="mt-6 grid gap-px overflow-hidden rounded-md border border-border bg-border text-left sm:grid-cols-3">
          {doors.map((door) => (
            <button
              key={door.title}
              type="button"
              onClick={onAdd}
              className="bg-surface p-4 text-left transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40"
            >
              <door.icon className="size-4 text-foreground" strokeWidth={1.75} aria-hidden="true" />
              <span className="mt-2.5 block text-[13px] font-medium text-foreground">{door.title}</span>
              <span className="mt-1 block text-[12px] leading-snug text-muted-foreground">{door.copy}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

/** A titled group of model rows for the Models section. */
type ModelGroup = { key: string; title: string; models: AiModel[] }

/**
 * Every model belongs to a provider row (`provider_id` is always present —
 * catalog entries hang off the native row, discovered/manual ones off
 * theirs), so grouping is uniform: one fieldset per row with models.
 */
function buildModelGroups(models: AiModel[], providers: AiProvider[]): ModelGroup[] {
  return providers
    .map((provider) => ({
      key: provider.id,
      title: providerTitle(provider),
      models: models.filter((m) => m.provider_id === provider.id),
    }))
    .filter((g) => g.models.length > 0)
}

/**
 * Models + defaults + additional instructions — the mutable body of
 * PUT /ai/settings, edited locally and saved as one unit.
 */
function PreferencesForm({
  settings,
  models,
  providers,
  onNotice,
}: {
  settings: AiSettings
  models: AiModel[]
  providers: AiProvider[]
  onNotice: (n: Notice) => void
}) {
  const smartId = useId()
  const fastId = useId()
  const instructionsId = useId()
  const [save, { isLoading }] = useUpdateAiSettingsMutation()

  const [enabledIds, setEnabledIds] = useState<string[]>(settings.enabled_model_ids)
  const [smart, setSmart] = useState(settings.default_smart_model)
  const [fast, setFast] = useState(settings.default_fast_model)
  const [instructions, setInstructions] = useState(settings.additional_instructions)

  const dirty =
    smart !== settings.default_smart_model ||
    fast !== settings.default_fast_model ||
    instructions !== settings.additional_instructions ||
    enabledIds.length !== settings.enabled_model_ids.length ||
    enabledIds.some((id) => !settings.enabled_model_ids.includes(id))

  // An empty selection means "everything is enabled" — so the defaults offer
  // the whole catalog in that case, otherwise just the checked models.
  const enabledModels = enabledIds.length === 0 ? models : models.filter((m) => enabledIds.includes(m.id))

  function toggleModel(id: string) {
    setEnabledIds((prev) => (prev.includes(id) ? prev.filter((m) => m !== id) : [...prev, id]))
  }

  async function onSave() {
    const result = await save({
      aiSettingsUpdate: {
        default_smart_model: smart,
        default_fast_model: fast,
        enabled_model_ids: enabledIds,
        additional_instructions: instructions,
      },
    })
    if ('error' in result) {
      onNotice({
        tone: 'error',
        text:
          httpStatus(result.error) === 400
            ? 'The server rejected these settings — a model choice may no longer be available.'
            : "Couldn't save AI settings. Please try again.",
      })
    } else {
      onNotice({ tone: 'ok', text: 'AI settings saved.' })
    }
  }

  const groups = buildModelGroups(models, providers)

  return (
    <>
      <SectionBar label="Models" count={models.length} />
      <p className="border-b border-border px-5 py-2.5 text-[12px] text-muted-foreground">
        {enabledIds.length === 0
          ? 'No models are individually selected, so every configured model is enabled. Check specific models to restrict the workspace to just those.'
          : `${enabledIds.length} of ${models.length} models enabled for this workspace.`}
      </p>
      {groups.length === 0 ? (
        <EmptyBlock
          className="py-10"
          title="No models yet"
          description="Connect a provider above — its models appear here, ready to enable."
        />
      ) : (
        groups.map((group) => (
          <fieldset key={group.key} className="border-b border-border px-5 py-3.5">
            <legend className="float-left mb-2 w-full font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">
              {group.title}
            </legend>
            <div className="flex flex-col gap-2 clear-both">
              {group.models.map((model) => (
                <label key={model.id} className="flex cursor-pointer items-start gap-2.5 text-[13px] text-foreground">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4 accent-primary"
                    checked={enabledIds.includes(model.id)}
                    onChange={() => toggleModel(model.id)}
                  />
                  <span className="min-w-0">
                    <span className="flex flex-wrap items-center gap-2">
                      {model.label}
                      {model.source === 'custom' && (
                        <span className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground">
                          Custom
                        </span>
                      )}
                    </span>
                    <span className="block font-mono text-[11px] text-muted-foreground">
                      {formatTokens(model.context_window_tokens)} context
                      {model.supports_reasoning ? ' · reasoning' : ''}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
        ))
      )}

      <SectionBar label="Defaults" />
      <div className="grid gap-4 border-b border-border px-5 py-4 sm:grid-cols-2">
        <div>
          <Label htmlFor={smartId}>Default smart model</Label>
          <Select id={smartId} className="mt-1.5" value={smart} onChange={(e) => setSmart(e.target.value)}>
            <ModelOptions sentinel={DEFAULT_SMART_MODEL} current={smart} enabledModels={enabledModels} models={models} />
          </Select>
          <p className="mt-1 text-[11px] text-muted-foreground">Used for chat and multi-step agent work.</p>
        </div>
        <div>
          <Label htmlFor={fastId}>Default fast model</Label>
          <Select id={fastId} className="mt-1.5" value={fast} onChange={(e) => setFast(e.target.value)}>
            <ModelOptions sentinel={DEFAULT_FAST_MODEL} current={fast} enabledModels={enabledModels} models={models} />
          </Select>
          <p className="mt-1 text-[11px] text-muted-foreground">Used for quick tasks like naming threads.</p>
        </div>
      </div>

      <SectionBar label="Additional instructions" />
      <div className="border-b border-border px-5 py-4">
        <Label htmlFor={instructionsId}>Workspace instructions (optional)</Label>
        <Textarea
          id={instructionsId}
          className="mt-1.5 min-h-28"
          placeholder="e.g. Always write outreach copy in a friendly, concise tone. Our ICP is seed-stage SaaS founders."
          value={instructions}
          onChange={(e) => setInstructions(e.target.value)}
        />
        <p className="mt-1 text-[11px] text-muted-foreground">
          Appended to the assistant's instructions for every conversation in this workspace.
        </p>
      </div>

      <div className="flex items-center gap-3 px-5 py-4">
        <Button variant="primary" size="sm" disabled={!dirty || isLoading} onClick={() => void onSave()}>
          {isLoading && <Loader2 className="size-3.5 animate-spin" />}
          Save changes
        </Button>
        {dirty && <span className="text-[12px] text-muted-foreground">Unsaved changes</span>}
      </div>
    </>
  )
}

/**
 * Options for a default-model select: the "Auto" sentinel, the enabled models,
 * and — if the saved value is no longer among them — the saved value itself,
 * so the control never renders with a value missing from its options.
 */
function ModelOptions({
  sentinel,
  current,
  enabledModels,
  models,
}: {
  sentinel: string
  current: string
  enabledModels: AiModel[]
  models: AiModel[]
}) {
  const orphaned =
    current !== sentinel && !enabledModels.some((m) => m.id === current)
      ? { id: current, label: models.find((m) => m.id === current)?.label ?? current }
      : null

  return (
    <>
      <option value={sentinel}>Auto (recommended)</option>
      {enabledModels.map((model) => (
        <option key={model.id} value={model.id}>
          {model.label}
        </option>
      ))}
      {orphaned && <option value={orphaned.id}>{orphaned.label} (not enabled)</option>}
    </>
  )
}

function LoadingSections() {
  return (
    <div>
      <div className="border-b border-border px-5 py-2.5">
        <Skeleton className="h-3 w-24" />
      </div>
      {[0, 1, 2].map((i) => (
        <div key={i} className="border-b border-border px-5 py-4">
          <Skeleton className="h-4 w-44" />
          <Skeleton className="mt-2 h-3 w-64" />
          <Skeleton className="mt-4 h-9 w-full max-w-md" />
        </div>
      ))}
    </div>
  )
}
