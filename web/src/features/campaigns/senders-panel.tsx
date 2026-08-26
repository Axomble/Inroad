import { useEffect, useState } from 'react'
import { GitBranch } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionBar } from '@/components/layout/page'
import { HealthBadge } from '@/components/shared/health-badge'
import { httpStatus } from '@/lib/rtk-error'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
// Read-only reference data from another feature's query hook — the documented
// loophole at the top of ./api.ts. No cross-feature component import.
import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useGetCampaignSendersQuery, useUpdateCampaignSendersMutation } from './api'
import type { RotationMode } from './api'
import { FaultDomainExposure } from './fault-domain-exposure'
import {
  ROTATION_MODES,
  capacityLabel,
  fromDraft,
  gatedReason,
  reducedCapReason,
  rotationModeDescription,
  senderErrorMessage,
  toDraft,
} from './sender-draft'
import type { DraftSender } from './sender-draft'

/**
 * The campaign's sender pool: which mailboxes it sends from, and how a contact
 * is assigned one of them.
 *
 * The pool is resolved once per *contact*, at its first send, and every later
 * step reuses that mailbox — a follow-up is a reply in the same thread, so it
 * cannot come from a different address. The panel says this out loud, because
 * "weight" otherwise reads as a per-send split, which it is not.
 */
export function SendersPanel({ campaignId }: { campaignId: string }) {
  const { data, isLoading, error } = useGetCampaignSendersQuery({ id: campaignId })
  const { data: mailboxes, isLoading: mailboxesLoading, error: mailboxesError } = useListMailboxesQuery()
  const [save, { isLoading: isSaving, error: saveError, isSuccess }] = useUpdateCampaignSendersMutation()

  const [mode, setMode] = useState<RotationMode>('weighted')
  const [rows, setRows] = useState<DraftSender[]>([])
  const [problem, setProblem] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  // Seed the editor once both the pool and the mailbox list arrive, and re-seed
  // after a save so the form reflects what was actually persisted. Guarded on
  // `dirty` so a background refetch can't discard edits in progress.
  useEffect(() => {
    if (!data || dirty) return
    setMode(data.rotation_mode)
    setRows(toDraft(data, mailboxes))
  }, [data, mailboxes, dirty])

  function editRow(mailboxId: string, patch: Partial<DraftSender>) {
    setRows((current) => current.map((row) => (row.mailbox_id === mailboxId ? { ...row, ...patch } : row)))
    setDirty(true)
    setProblem(null)
  }

  async function onSave() {
    const result = fromDraft(mode, rows)
    if ('problem' in result) {
      setProblem(result.problem)
      return
    }
    try {
      await save({ id: campaignId, campaignSenderPoolRequest: result.pool }).unwrap()
      setDirty(false)
      setProblem(null)
    } catch {
      // The rejected promise is surfaced through `saveError` below; swallowing it
      // here only stops the unhandled rejection, it isn't the error handling.
    }
  }

  if (isLoading || mailboxesLoading) {
    return (
      <div className="border-b border-border">
        <SectionBar label="Senders" />
        <div className="space-y-2 px-5 py-4">
          <Skeleton className="h-5 w-48" />
          <Skeleton className="h-5 w-64" />
        </div>
      </div>
    )
  }

  // A failed load must not render as an empty pool: "no senders" is a real,
  // actionable state and would be indistinguishable from a broken request.
  if (error || mailboxesError) {
    const failed = error ?? mailboxesError
    return (
      <div className="border-b border-border">
        <SectionBar label="Senders" />
        <div role="alert" className="px-5 py-6 text-sm text-danger">
          Couldn't load the senders{httpStatus(failed) ? ` (${httpStatus(failed)})` : ''} — try again.
        </div>
      </div>
    )
  }

  const includedCount = rows.filter((row) => row.included && row.enabled).length

  return (
    <div className="border-b border-border">
      <SectionBar label="Senders">
        {dirty && (
          <Button size="xs" onClick={() => void onSave()} disabled={isSaving}>
            {isSaving ? 'Saving…' : 'Save senders'}
          </Button>
        )}
      </SectionBar>

      <div className="space-y-4 px-5 py-4">
        <div className="max-w-xs space-y-1.5">
          <Label htmlFor="rotation-mode">Rotation mode</Label>
          <Select
            id="rotation-mode"
            value={mode}
            onChange={(e) => {
              setMode(e.target.value as RotationMode)
              setDirty(true)
              setProblem(null)
            }}
          >
            {ROTATION_MODES.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </Select>
          <p className="text-xs text-muted-foreground">{rotationModeDescription(mode)}</p>
        </div>

        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No active mailboxes to send from. Connect a mailbox first — until then this campaign sends from
            the mailbox it was created with.
          </p>
        ) : (
          <div className="space-y-1.5">
            <p className="text-xs text-muted-foreground">
              Tick a mailbox to put it in the pool. Clearing <em>In rotation</em> holds one out of new
              assignments while keeping its weight and history, so it can come back without resetting the
              spread.
            </p>
            <ul className="divide-y divide-border rounded-md border border-border">
              {rows.map((row) => (
                <SenderRow key={row.mailbox_id} row={row} onChange={editRow} />
              ))}
            </ul>
          </div>
        )}

        {/* Directly under the pool it measures: the only action it ever asks
            for is a change to the ticks above it. Reads the server's pool, never
            the draft — an unsaved tick has not moved a single contact. */}
        <FaultDomainExposure pool={data} />

        <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
          <GitBranch className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span>
            Follow-ups always send from the mailbox that started the thread — a reply from a different
            address would break threading. Rotation spreads <strong className="font-medium">contacts</strong>{' '}
            across the pool, not individual sends, so weights shift who gets the next contact.
          </span>
        </p>

        {problem && (
          <p role="alert" className="text-sm text-danger">
            {problem}
          </p>
        )}
        {saveError && (
          <p role="alert" className="text-sm text-danger">
            {senderErrorMessage(saveError)}
          </p>
        )}
        {isSuccess && !dirty && !saveError && (
          <p className="text-sm text-muted-foreground">
            Senders saved. {includedCount === 1 ? '1 mailbox is' : `${includedCount} mailboxes are`} in
            rotation for contacts assigned from now on.
          </p>
        )}
      </div>
    </div>
  )
}

/**
 * One mailbox row. Two booleans, deliberately: excluding removes the mailbox
 * from the pool entirely, while leaving it included but out of rotation keeps its
 * assignment history so it can be brought back without resetting the spread.
 */
function SenderRow({
  row,
  onChange,
}: {
  row: DraftSender
  onChange: (mailboxId: string, patch: Partial<DraftSender>) => void
}) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2">
      <input
        type="checkbox"
        aria-label={`Include ${row.email} in the pool`}
        className="size-4 shrink-0 accent-current"
        checked={row.included}
        onChange={(e) => onChange(row.mailbox_id, { included: e.target.checked })}
      />

      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1.5">
          <span className={cn('min-w-0 truncate text-sm', row.included ? 'text-foreground' : 'text-muted-foreground')}>
            {row.email}
          </span>
          {/* Only for a mailbox that is actually warming up: no state means the
              warmup engine has no opinion, which is not a claim of health. */}
          {row.healthState && <HealthBadge state={row.healthState} className="shrink-0" />}
        </span>
        <span className="block text-xs text-muted-foreground">
          {row.provider ?? 'smtp'}
          {row.status && row.status !== 'active' ? ` · ${row.status}` : ''}
          {' · '}
          {row.assignedCount === 1 ? '1 contact' : `${row.assignedCount} contacts`}
          {row.lastAssignedAt ? ` · last assigned ${relativeTime(row.lastAssignedAt)}` : ' · never assigned'}
        </span>
        <SenderCapacity row={row} />
      </span>

      <span className="flex items-center gap-1.5">
        <span className="text-xs text-muted-foreground">Weight</span>
        <Input
          type="number"
          min={1}
          max={100}
          aria-label={`Weight for ${row.email}`}
          className="h-8 w-16"
          disabled={!row.included}
          value={row.weight}
          onChange={(e) => onChange(row.mailbox_id, { weight: e.target.value })}
        />
      </span>

      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <input
          type="checkbox"
          aria-label={`In rotation for ${row.email}`}
          className="size-4 accent-current"
          disabled={!row.included}
          checked={row.included && row.enabled}
          onChange={(e) => onChange(row.mailbox_id, { enabled: e.target.checked })}
        />
        In rotation
      </label>
    </li>
  )
}

/**
 * What this mailbox has sent today against what it may send, and — when it is
 * sending nothing — why.
 *
 * This is the whole reason the health/capacity fields exist on the row. Without
 * it, warmup silently gating a mailbox shows up only as a campaign running slower
 * than it was configured to, with nothing on screen accounting for the shortfall.
 * Read-only throughout: these are the engine's numbers, not settings.
 */
function SenderCapacity({ row }: { row: DraftSender }) {
  const capacity = capacityLabel(row)
  const gated = gatedReason(row)
  const note = gated ?? reducedCapReason(row)
  if (!capacity && !note) return null
  return (
    <span className="mt-0.5 flex flex-wrap items-baseline gap-x-1.5 text-xs">
      {capacity && <span className="font-mono text-foreground">{capacity}</span>}
      {capacity && note && (
        <span className="text-faint" aria-hidden="true">
          ·
        </span>
      )}
      {note && <span className={gated ? 'text-danger' : 'text-muted-foreground'}>{note}</span>}
    </span>
  )
}
