import { useEffect, useState } from 'react'
import { GitBranch } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionBar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
// Read-only reference data from another feature's query hook — the documented
// loophole at the top of ./api.ts. No cross-feature component import.
import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useGetCampaignSendersQuery, useUpdateCampaignSendersMutation } from './api'
import type { RotationMode } from './api'
import {
  ROTATION_MODES,
  fromDraft,
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
          <select
            id="rotation-mode"
            className="h-9 w-full rounded-md border border-border bg-surface px-2 text-sm"
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
          </select>
          <p className="text-xs text-muted">{rotationModeDescription(mode)}</p>
        </div>

        {rows.length === 0 ? (
          <p className="text-sm text-muted">
            No active mailboxes to send from. Connect a mailbox first — until then this campaign sends from
            the mailbox it was created with.
          </p>
        ) : (
          <div className="space-y-1.5">
            <p className="text-xs text-muted">
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

        <p className="flex items-start gap-1.5 text-xs text-muted">
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
          <p className="text-sm text-muted">
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
        <span className={cn('block truncate text-sm', row.included ? 'text-foreground' : 'text-muted')}>
          {row.email}
        </span>
        <span className="block text-xs text-muted">
          {row.provider ?? 'smtp'}
          {row.status && row.status !== 'active' ? ` · ${row.status}` : ''}
          {' · '}
          {row.assignedCount === 1 ? '1 contact' : `${row.assignedCount} contacts`}
          {row.lastAssignedAt ? ` · last assigned ${relativeTime(row.lastAssignedAt)}` : ' · never assigned'}
        </span>
      </span>

      <span className="flex items-center gap-1.5">
        <span className="text-xs text-muted">Weight</span>
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

      <label className="flex items-center gap-1.5 text-xs text-muted">
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
