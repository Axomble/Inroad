import { useEffect, useMemo, useState } from 'react'
import { Loader2, Plus, Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
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
  useCreateAiModelMutation,
  useDiscoverAiProviderModelsMutation,
  type AiDiscoveredModel,
  type AiProvider,
} from './api'
import { formatTokens, providerTitle } from './provider-format'

/** Search kicks in once the list is long enough that scanning stops working. */
const SEARCH_THRESHOLD = 8

/**
 * Discovery: ask the provider for its own model list and let the operator
 * pick. Candidates with metadata are multi-selectable and save straight
 * through; bare-id candidates (Ollama, vLLM) hand off to the manual form
 * prefilled. Unsupported kinds (bedrock/vertex in A1) and failures both land
 * on the manual path — discovery is a shortcut, never a wall.
 */
export function DiscoverModelsDialog({
  provider,
  existingNames,
  onClose,
  onManualAdd,
  onNotice,
}: {
  provider: AiProvider
  /** Names already added to this provider — shown as such, not re-addable. */
  existingNames: string[]
  onClose: () => void
  /** Switch to the manual Add-model dialog, optionally seeded by a candidate. */
  onManualAdd: (prefill?: AiDiscoveredModel) => void
  onNotice: (n: Notice) => void
}) {
  const [discover, discovery] = useDiscoverAiProviderModelsMutation()
  const [createModel] = useCreateAiModelMutation()
  const [selected, setSelected] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    void discover({ id: provider.id })
  }, [discover, provider.id])

  const candidates = useMemo(() => discovery.data?.models ?? [], [discovery.data])
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return candidates
    return candidates.filter(
      (c) => c.name.toLowerCase().includes(needle) || (c.label ?? '').toLowerCase().includes(needle),
    )
  }, [candidates, query])

  function toggle(name: string) {
    setSelected((prev) => (prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]))
  }

  async function onAddSelected() {
    const picked = candidates.filter((c) => selected.includes(c.name))
    if (picked.length === 0) return
    setSaving(true)

    const results = await Promise.all(
      picked.map((candidate) => {
        // context is guaranteed here — only metadata candidates are selectable.
        const context = candidate.context_window_tokens ?? 128000
        return createModel({
          aiModelCreateRequest: {
            provider_id: provider.id,
            name: candidate.name,
            label: candidate.label?.trim() ? candidate.label : candidate.name,
            context_window_tokens: context,
            max_output_tokens: candidate.max_output_tokens ?? Math.min(16000, context),
            supports_reasoning: false,
            ...(candidate.input_cost_per_mtok != null ? { input_cost_per_mtok: candidate.input_cost_per_mtok } : {}),
            ...(candidate.output_cost_per_mtok != null ? { output_cost_per_mtok: candidate.output_cost_per_mtok } : {}),
          },
        })
      }),
    )
    const failures = results.filter((result) => 'error' in result).length
    setSaving(false)

    if (failures > 0) {
      onNotice({
        tone: 'error',
        text: `${picked.length - failures} of ${picked.length} models added — ${failures} failed. Try those again.`,
      })
    } else {
      onNotice({
        tone: 'ok',
        text: `${picked.length} ${picked.length === 1 ? 'model' : 'models'} added to ${providerTitle(provider)}.`,
      })
    }
    onClose()
  }

  const unsupported = discovery.isSuccess && !discovery.data.supported

  return (
    <AlertDialog open onOpenChange={(next) => !next && !saving && onClose()}>
      <AlertDialogContent className="max-h-[85vh] overflow-y-auto">
        <AlertDialogHeader>
          <AlertDialogTitle>Models on {providerTitle(provider)}</AlertDialogTitle>
          <AlertDialogDescription>
            {unsupported
              ? 'Model discovery for this provider arrives with the runtime — add model IDs manually for now.'
              : 'Fetched from the provider itself. Pick the ones this workspace should use.'}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {discovery.isLoading || discovery.isUninitialized ? (
          <div className="flex flex-col gap-2" aria-label="Fetching models">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : discovery.isError ? (
          <div className="flex flex-col items-start gap-3">
            <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
              Couldn't reach the provider for its model list — it may be down, or the key may be wrong. You
              can retry, or add models manually.
            </p>
            <Button variant="outline" size="sm" onClick={() => void discover({ id: provider.id })}>
              Retry
            </Button>
          </div>
        ) : unsupported ? null : candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            The provider reported no models. Add one manually if you know its ID.
          </p>
        ) : (
          <div className="flex flex-col gap-2">
            {candidates.length > SEARCH_THRESHOLD && (
              <div className="relative">
                <Search
                  className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-faint"
                  aria-hidden="true"
                />
                <Input
                  aria-label="Search models"
                  className="pl-8"
                  placeholder={`Search ${candidates.length} models…`}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                />
              </div>
            )}

            <div className="max-h-72 overflow-y-auto rounded-md border border-border">
              {filtered.length === 0 ? (
                <p className="px-3 py-6 text-center text-sm text-muted-foreground">No models match “{query}”.</p>
              ) : (
                filtered.map((candidate) => {
                  const added = existingNames.includes(candidate.name)
                  const bare = candidate.context_window_tokens == null
                  return (
                    <div
                      key={candidate.name}
                      className="flex items-center gap-2.5 border-b border-border/60 px-3 py-2 last:border-b-0"
                    >
                      {added || bare ? (
                        <span className="size-4 shrink-0" aria-hidden="true" />
                      ) : (
                        <input
                          type="checkbox"
                          className="size-4 shrink-0 accent-primary"
                          aria-label={`Select ${candidate.label ?? candidate.name}`}
                          checked={selected.includes(candidate.name)}
                          onChange={() => toggle(candidate.name)}
                          disabled={saving}
                        />
                      )}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[13px] text-foreground">
                          {candidate.label?.trim() ? candidate.label : candidate.name}
                        </span>
                        <span className="block truncate font-mono text-[10.5px] text-faint">
                          {candidate.name}
                          {candidate.context_window_tokens != null &&
                            ` · ${formatTokens(candidate.context_window_tokens)}`}
                          {candidate.input_cost_per_mtok != null &&
                            ` · $${candidate.input_cost_per_mtok}/M in`}
                        </span>
                      </span>
                      {added ? (
                        <span className="font-mono text-[10.5px] uppercase tracking-[0.1em] text-faint">Added</span>
                      ) : bare ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={saving}
                          aria-label={`Add ${candidate.name} manually`}
                          onClick={() => onManualAdd(candidate)}
                        >
                          <Plus className="size-3.5" />
                          Add…
                        </Button>
                      ) : null}
                    </div>
                  )
                })
              )}
            </div>
          </div>
        )}

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={() => onManualAdd()} disabled={saving}>
            Add manually
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>
            Close
          </Button>
          {!unsupported && !discovery.isError && candidates.length > 0 && (
            <Button variant="primary" size="sm" disabled={selected.length === 0 || saving} onClick={() => void onAddSelected()}>
              {saving && <Loader2 className="size-3.5 animate-spin" />}
              Add {selected.length > 0 ? selected.length : ''} selected
            </Button>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
