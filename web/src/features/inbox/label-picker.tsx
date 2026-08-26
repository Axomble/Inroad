import { useMemo, useState } from 'react'
import { Tag, Check, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { httpStatus } from '@/lib/rtk-error'
import { cn } from '@/lib/utils'
import {
  useListInboxLabelsQuery,
  useCreateInboxLabelMutation,
  useAssignInboxThreadLabelMutation,
  useUnassignInboxThreadLabelMutation,
  type InboxLabel,
} from './api'

/** Mirrors the API's own cap, so the field cannot submit a name the server will refuse. */
const MAX_LABEL_NAME_LENGTH = 40

/**
 * Search-or-create label picker for one thread.
 *
 * Typing filters the workspace's labels; if nothing matches exactly, the first
 * item becomes "Create <name>". Creation goes through POST /inbox/labels, which
 * is itself search-or-create server-side — so two members typing the same new
 * name concurrently both end up on one label rather than one of them seeing a
 * conflict.
 *
 * Applied labels are toggles: selecting an applied one removes it.
 */
export function LabelPicker({
  threadId,
  applied,
}: {
  threadId: string
  /** Optional for the same codegen reason as LabelChips' own prop. */
  applied: readonly InboxLabel[] | undefined
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  // Only fetch the taxonomy once the menu is actually opened: the picker sits on
  // every thread, and a list request per rendered row would be wasteful.
  const { data, isLoading, error: listError } = useListInboxLabelsQuery(undefined, { skip: !open })
  const [createLabel, { isLoading: isCreating, error: createError }] = useCreateInboxLabelMutation()
  const [assign, { error: assignError }] = useAssignInboxThreadLabelMutation()
  const [unassign, { error: unassignError }] = useUnassignInboxThreadLabelMutation()
  const error = createError ?? assignError ?? unassignError ?? listError
  const errorMessage = labelErrorMessage(error)

  // Both fallbacks are memoized, not written inline: `applied ?? []` and
  // `data?.labels ?? []` would each be a fresh array literal on every render
  // while the source is undefined, so every memo depending on them would
  // recompute each render and defeat its own purpose.
  const appliedLabels = useMemo(() => applied ?? [], [applied])
  const appliedIds = useMemo(() => new Set(appliedLabels.map((l) => l.id)), [appliedLabels])

  const trimmed = query.trim()
  const labels = useMemo(() => data?.labels ?? [], [data])
  const matches = useMemo(() => {
    const needle = trimmed.toLowerCase()
    if (!needle) return labels
    return labels.filter((l) => l.name.toLowerCase().includes(needle))
  }, [labels, trimmed])

  // Offer creation only when the typed name is not already a label — compared
  // case-insensitively, the same way the server compares it, so the picker never
  // offers to "create" something that would resolve to an existing label.
  const exactExists = labels.some((l) => l.name.toLowerCase() === trimmed.toLowerCase())
  const canCreate = trimmed !== '' && !exactExists && trimmed.length <= MAX_LABEL_NAME_LENGTH

  const onOpenChange = (next: boolean) => {
    if (next) setQuery('')
    setOpen(next)
  }

  const toggle = (label: InboxLabel) => {
    if (appliedIds.has(label.id)) void unassign({ id: threadId, labelId: label.id })
    else void assign({ id: threadId, labelId: label.id })
  }

  const create = async () => {
    if (!canCreate) return
    // `.unwrap()` REJECTS on a failed request, so both awaits are guarded: an
    // unhandled rejection here would escape as a runtime error while the
    // mutation's own `error` state — the thing that renders the alert below —
    // went unread. Nothing is swallowed: the error is already in RTK Query's
    // state, which is exactly where the alert reads it from.
    try {
      const created = await createLabel({ upsertInboxLabelRequest: { name: trimmed } }).unwrap()
      // The response is the created OR resolved label, so applying it is correct
      // either way — including when a colleague created the same name first.
      await assign({ id: threadId, labelId: created.id }).unwrap()
      setQuery('')
    } catch {
      // Intentionally empty: `createError`/`assignError` already hold this, and
      // the query must not be cleared so the operator can retry or edit it.
    }
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <DropdownMenu open={open} onOpenChange={onOpenChange}>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="xs" aria-label="Label this thread">
            <Tag className="size-3.5" />
            {appliedLabels.length > 0
              ? `${appliedLabels.length} label${appliedLabels.length === 1 ? '' : 's'}`
              : 'Label'}
          </Button>
        </DropdownMenuTrigger>

        <DropdownMenuContent align="end" className="w-64">
          <div className="px-2 pt-1 pb-2">
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value.slice(0, MAX_LABEL_NAME_LENGTH))}
              placeholder="Search or create a label…"
              aria-label="Search or create a label"
              className="h-8 text-[12px]"
              onKeyDown={(e) => {
                if (e.key === 'Enter' && canCreate) {
                  e.preventDefault()
                  void create()
                }
              }}
            />
          </div>

          {canCreate && (
            <DropdownMenuItem disabled={isCreating} onSelect={(e) => { e.preventDefault(); void create() }}>
              <Plus className="size-3.5" />
              <span className="truncate">Create “{trimmed}”</span>
            </DropdownMenuItem>
          )}

          {isLoading ? (
            <p className="px-2 py-2 text-[11px] text-faint">Loading labels…</p>
          ) : matches.length === 0 && !canCreate ? (
            <p className="px-2 py-2 text-[11px] text-faint">
              {trimmed ? 'No labels match.' : 'No labels yet — type a name to create one.'}
            </p>
          ) : (
            <>
              {matches.length > 0 && <DropdownMenuSeparator />}
              {matches.map((label) => {
                const isApplied = appliedIds.has(label.id)
                return (
                  <DropdownMenuItem
                    key={label.id}
                    // Kept open so several labels can be applied in one visit —
                    // filing usually means more than one.
                    onSelect={(e) => { e.preventDefault(); toggle(label) }}
                  >
                    <span
                      className="size-2 shrink-0 rounded-full"
                      style={{ backgroundColor: label.color }}
                      aria-hidden="true"
                    />
                    <span className="min-w-0 flex-1 truncate">{label.name}</span>
                    <Check className={cn('size-3.5 shrink-0', isApplied ? 'opacity-100' : 'opacity-0')} />
                    {isApplied && <span className="sr-only">applied</span>}
                  </DropdownMenuItem>
                )
              })}
            </>
          )}

          {trimmed.length >= MAX_LABEL_NAME_LENGTH && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuLabel className="text-warn">
                Label names are capped at {MAX_LABEL_NAME_LENGTH} characters.
              </DropdownMenuLabel>
            </>
          )}

          {/* Rendered INSIDE the menu as well as outside it. An open Radix menu
              marks the rest of the page aria-hidden, so an error shown only
              below would be invisible to a screen reader at the exact moment
              the operator is acting — and the failing action is one taken from
              inside this menu. */}
          {errorMessage && (
            <>
              <DropdownMenuSeparator />
              <p role="alert" className="px-2 py-1.5 text-[11px] text-danger">
                {errorMessage}
              </p>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {/* And outside it, for the case where the menu has since closed — a
          failed assign/unassign leaves no menu open to carry the message. */}
      {errorMessage && !open && (
        <p role="alert" className="text-[11px] text-danger">
          {errorMessage}
        </p>
      )}
    </div>
  )
}

/**
 * Human copy for a failed label action, or undefined when there is none.
 *
 * 422 is the one status with a cause the operator can act on (the workspace's
 * label cap); everything else reports its status so a bug report can name it.
 */
function labelErrorMessage(error: unknown): string | undefined {
  if (error === undefined) return undefined
  const status = httpStatus(error)
  if (status === 422) return 'This workspace has reached its label limit.'
  return `Couldn't update labels${status ? ` (${status})` : ''}.`
}
