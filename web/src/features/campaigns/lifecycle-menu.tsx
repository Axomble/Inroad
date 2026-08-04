import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, MoreVertical } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import type { Campaign } from '@/store/api'
import { useDeleteCampaignMutation, useRenameCampaignMutation } from './api'
import { lifecycleErrorMessage, type PauseResumeController } from './lifecycle-actions'
import { StopClickBubble } from './stop-click-bubble'

const renameSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Name is required')
    .max(200, 'Name must be 200 characters or fewer'),
})
type RenameValues = z.infer<typeof renameSchema>

/** The `{"error": "…"}` envelope the API writes, read through the typed seam. */
function serverReason(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const reason = (error.data as { error?: string } | undefined)?.error
  return typeof reason === 'string' && reason.trim() !== '' ? reason : undefined
}

function renameErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  const reason = serverReason(error)
  if (status === 400) return reason ?? 'Name must be between 1 and 200 characters.'
  if (status === 404) return 'This campaign no longer exists — refresh the page.'
  return "Couldn't rename this campaign. Please try again."
}

function InlineErrorBanner({ message }: { message: string }) {
  return (
    <div
      role="alert"
      className="absolute right-0 top-full z-10 mt-1 w-64 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
    >
      {message}
    </div>
  )
}

/**
 * The confirm-pause `AlertDialog` and its inline error banner for one shared
 * `usePauseResume` instance (see `./lifecycle-actions`). Rendered exactly once
 * by whichever page/row owns that instance — even when it's driven by two
 * separate trigger controls (the detail page's dedicated
 * `CampaignStatusButton` and `LifecycleMenu`'s own "Pause campaign" item) — so
 * there is structurally only ever one dialog that can open and one mutation
 * trigger that can fire, never two independent ones that can stack or
 * double-fire.
 */
export function PauseResumeDialog({
  campaign,
  pauseResume,
}: {
  campaign: Campaign
  pauseResume: PauseResumeController
}) {
  const name = campaign.name ?? 'this campaign'
  return (
    <StopClickBubble>
      {pauseResume.error && <InlineErrorBanner message={pauseResume.error} />}
      <AlertDialog open={pauseResume.confirmPause} onOpenChange={pauseResume.setConfirmPause}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Pause &ldquo;{name}&rdquo;?</AlertDialogTitle>
            <AlertDialogDescription>
              Pausing stops new sends immediately. Threads already in flight resume where they left off
              when you resume the campaign.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pauseResume.isPausing}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={pauseResume.isPausing}
              onClick={(e) => {
                e.preventDefault()
                void pauseResume.onPause()
              }}
            >
              {pauseResume.isPausing && <Loader2 className="size-4 animate-spin" />}
              Pause campaign
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </StopClickBubble>
  )
}

/** A small dialog with a single input, reusing Input + Button rather than a bespoke form. */
function RenameDialog({
  open,
  onOpenChange,
  campaign,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  campaign: Campaign
}) {
  const [rename, { isLoading, error }] = useRenameCampaignMutation()
  const nameId = useId()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<RenameValues>({
    resolver: zodResolver(renameSchema),
    defaultValues: { name: campaign.name ?? '' },
  })

  async function onSubmit(values: RenameValues) {
    const id = campaign.id ?? ''
    const result = await rename({ id, renameCampaignRequest: { name: values.name } })
    if ('data' in result && result.data) onOpenChange(false)
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {/* `contents` keeps the header/body/footer as direct children of the
            AlertDialogContent grid, so wrapping them in a form doesn't collapse
            the layout's vertical rhythm into one grid cell. */}
        <form onSubmit={handleSubmit(onSubmit)} noValidate className="contents">
          <AlertDialogHeader>
            <AlertDialogTitle>Rename campaign</AlertDialogTitle>
          </AlertDialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={nameId}>Name</Label>
            <Input id={nameId} autoFocus aria-invalid={!!errors.name} {...register('name')} />
            {errors.name && (
              <span role="alert" className="text-xs text-danger">
                {errors.name.message}
              </span>
            )}
          </div>
          {error && (
            <p
              role="alert"
              className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
            >
              {renameErrorMessage(error)}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel type="button" disabled={isLoading}>
              Cancel
            </AlertDialogCancel>
            <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
              {isLoading && <Loader2 className="size-4 animate-spin" />}
              Save
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * Status-appropriate lifecycle actions for one campaign, behind a single
 * overflow menu: pause (running, behind a confirm — it stops sends
 * immediately), resume (paused, no confirm — resuming is always safe), delete
 * (draft only, destructive, behind a confirm naming the campaign), and rename
 * (every status).
 *
 * `pauseResume` is caller-owned (`usePauseResume` in `./lifecycle-actions`),
 * not created here — see `PauseResumeDialog`'s doc comment for why: it may be
 * shared with a sibling `CampaignStatusButton`, and each must drive the same
 * mutation trigger and the same confirm-dialog state.
 */
export function LifecycleMenu({
  campaign,
  pauseResume,
}: {
  campaign: Campaign
  pauseResume: PauseResumeController
}) {
  const id = campaign.id ?? ''
  const name = campaign.name ?? 'this campaign'
  const status = campaign.status

  const [remove, removeState] = useDeleteCampaignMutation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [renameOpen, setRenameOpen] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const busy = pauseResume.isPausing || pauseResume.isResuming || removeState.isLoading

  async function onDelete() {
    setDeleteError(null)
    const res = await remove({ id })
    setConfirmDelete(false)
    if ('error' in res) setDeleteError(lifecycleErrorMessage('delete', res.error))
  }

  return (
    <StopClickBubble>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${name}`} disabled={busy}>
            <MoreVertical className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {status === 'running' && (
            <DropdownMenuItem disabled={busy} onSelect={() => pauseResume.setConfirmPause(true)}>
              Pause campaign
            </DropdownMenuItem>
          )}
          {status === 'paused' && (
            <DropdownMenuItem
              className="text-accent-ink"
              disabled={busy}
              onSelect={() => void pauseResume.onResume()}
            >
              Resume campaign
            </DropdownMenuItem>
          )}

          <DropdownMenuItem onSelect={() => setRenameOpen(true)}>Rename…</DropdownMenuItem>

          {status === 'draft' && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" disabled={busy} onSelect={() => setConfirmDelete(true)}>
                Delete campaign
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {deleteError && <InlineErrorBanner message={deleteError} />}

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete &ldquo;{name}&rdquo;?</AlertDialogTitle>
            <AlertDialogDescription>
              This permanently deletes the draft campaign, its sequence, schedule and sender pool. This
              cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removeState.isLoading}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-destructive-foreground hover:bg-danger/90"
              disabled={removeState.isLoading}
              onClick={(e) => {
                e.preventDefault()
                void onDelete()
              }}
            >
              {removeState.isLoading && <Loader2 className="size-4 animate-spin" />}
              Delete campaign
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* `key` forces a fresh mount every time the dialog opens (Radix does not
          call `onOpenChange` for an externally-driven open, only for its own
          close requests, so there is no reliable "just opened" event to hook a
          manual reset into) — a remount gives fresh `useForm`/mutation state
          for free, so a rename that failed and was cancelled never shows last
          time's error banner on reopen. */}
      <RenameDialog
        key={renameOpen ? 'open' : 'closed'}
        open={renameOpen}
        onOpenChange={setRenameOpen}
        campaign={campaign}
      />
    </StopClickBubble>
  )
}

/**
 * The detail page's dedicated, visible pause/resume control — deliberately
 * not buried in `LifecycleMenu`'s overflow menu, since pausing or resuming is
 * the single most consequential action available while looking at one
 * campaign. Renders nothing outside running/paused (draft has nothing to
 * pause; done has nothing to resume). Shares its confirm dialog and mutation
 * trigger with `LifecycleMenu` via the caller-owned `pauseResume` controller —
 * render `PauseResumeDialog` once, alongside both, never one per control.
 */
export function CampaignStatusButton({
  campaign,
  pauseResume,
}: {
  campaign: Campaign
  pauseResume: PauseResumeController
}) {
  const status = campaign.status

  if (status !== 'running' && status !== 'paused') return null

  return status === 'running' ? (
    <Button
      variant="secondary"
      size="sm"
      disabled={pauseResume.isPausing}
      onClick={() => pauseResume.setConfirmPause(true)}
    >
      {pauseResume.isPausing && <Loader2 className="size-4 animate-spin" />}
      Pause
    </Button>
  ) : (
    <Button
      variant="secondary"
      size="sm"
      disabled={pauseResume.isResuming}
      onClick={() => void pauseResume.onResume()}
    >
      {pauseResume.isResuming && <Loader2 className="size-4 animate-spin" />}
      Resume
    </Button>
  )
}
