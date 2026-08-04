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
import {
  useDeleteCampaignMutation,
  usePauseCampaignMutation,
  useRenameCampaignMutation,
  useResumeCampaignMutation,
} from './api'

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

/**
 * Copy for a failed pause/resume/delete. A 409 here is a lifecycle-guard
 * rejection ("campaign is not running") that only the API can phrase
 * correctly for the status it actually saw, so its own reason wins over a
 * guess at the exact wording.
 */
function lifecycleErrorMessage(action: 'pause' | 'resume' | 'delete', error: unknown): string {
  const status = httpStatus(error)
  const reason = serverReason(error)
  if (status === 409) return reason ?? `This campaign can't be ${pastTense(action)} from its current status.`
  if (status === 404) return 'This campaign no longer exists — refresh the page.'
  return `Couldn't ${action} this campaign. Please try again.`
}

function pastTense(action: 'pause' | 'resume' | 'delete'): string {
  if (action === 'pause') return 'paused'
  if (action === 'resume') return 'resumed'
  return 'deleted'
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
 * Shared pause/resume behaviour: the mutation triggers, the confirm gate that
 * pause (but not resume) requires, and the resulting inline error. Reused by
 * the row/topbar overflow menu and the detail page's dedicated button so the
 * copy and the confirmation gate live in exactly one place.
 */
function usePauseResume(campaign: Campaign) {
  const id = campaign.id ?? ''
  const [pause, pauseState] = usePauseCampaignMutation()
  const [resume, resumeState] = useResumeCampaignMutation()
  const [confirmPause, setConfirmPause] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onPause() {
    setError(null)
    const res = await pause({ id })
    setConfirmPause(false)
    if ('error' in res) setError(lifecycleErrorMessage('pause', res.error))
  }

  async function onResume() {
    setError(null)
    const res = await resume({ id })
    if ('error' in res) setError(lifecycleErrorMessage('resume', res.error))
  }

  return {
    confirmPause,
    setConfirmPause,
    onPause,
    onResume,
    isPausing: pauseState.isLoading,
    isResuming: resumeState.isLoading,
    error,
  }
}

function PauseConfirmDialog({
  open,
  onOpenChange,
  name,
  busy,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  name: string
  busy: boolean
  onConfirm: () => void
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Pause &ldquo;{name}&rdquo;?</AlertDialogTitle>
          <AlertDialogDescription>
            Pausing stops new sends immediately. Threads already in flight resume where they left off when
            you resume the campaign.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(e) => {
              e.preventDefault()
              onConfirm()
            }}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}
            Pause campaign
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
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
    reset,
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
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        // Re-seed the input from the latest name every time the dialog opens,
        // so a stale draft from a previous open (or a rename elsewhere) never
        // shows through.
        if (next) reset({ name: campaign.name ?? '' })
        onOpenChange(next)
      }}
    >
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
 */
export function LifecycleMenu({ campaign }: { campaign: Campaign }) {
  const id = campaign.id ?? ''
  const name = campaign.name ?? 'this campaign'
  const status = campaign.status

  const pauseResume = usePauseResume(campaign)
  const [remove, removeState] = useDeleteCampaignMutation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [renameOpen, setRenameOpen] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const actionError = pauseResume.error ?? deleteError
  const busy = pauseResume.isPausing || pauseResume.isResuming || removeState.isLoading

  async function onDelete() {
    setDeleteError(null)
    const res = await remove({ id })
    setConfirmDelete(false)
    if ('error' in res) setDeleteError(lifecycleErrorMessage('delete', res.error))
  }

  return (
    // Stops a click anywhere in this menu (including portalled dropdown/dialog
    // content, which React still bubbles through this component's tree) from
    // reaching a row's own onClick — e.g. campaigns-page.tsx's row navigates on
    // click, and without this a confirmed delete would immediately navigate to
    // the campaign it just deleted.
    <div className="relative inline-flex" onClick={(e) => e.stopPropagation()}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${name}`} disabled={busy}>
            <MoreVertical className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {status === 'running' && (
            <DropdownMenuItem
              disabled={busy}
              onSelect={(e) => {
                e.preventDefault()
                pauseResume.setConfirmPause(true)
              }}
            >
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

          <DropdownMenuItem
            onSelect={(e) => {
              e.preventDefault()
              setRenameOpen(true)
            }}
          >
            Rename…
          </DropdownMenuItem>

          {status === 'draft' && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                disabled={busy}
                onSelect={(e) => {
                  e.preventDefault()
                  setConfirmDelete(true)
                }}
              >
                Delete campaign
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {actionError && <InlineErrorBanner message={actionError} />}

      <PauseConfirmDialog
        open={pauseResume.confirmPause}
        onOpenChange={pauseResume.setConfirmPause}
        name={name}
        busy={pauseResume.isPausing}
        onConfirm={() => void pauseResume.onPause()}
      />

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

      <RenameDialog open={renameOpen} onOpenChange={setRenameOpen} campaign={campaign} />
    </div>
  )
}

/**
 * The detail page's dedicated, visible pause/resume control — deliberately
 * not buried in `LifecycleMenu`'s overflow menu, since pausing or resuming is
 * the single most consequential action available while looking at one
 * campaign. Renders nothing outside running/paused (draft has nothing to
 * pause; done has nothing to resume).
 */
export function CampaignStatusButton({ campaign }: { campaign: Campaign }) {
  const name = campaign.name ?? 'this campaign'
  const status = campaign.status
  const pauseResume = usePauseResume(campaign)

  if (status !== 'running' && status !== 'paused') return null

  return (
    <div className="relative inline-flex">
      {status === 'running' ? (
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
      )}

      {pauseResume.error && <InlineErrorBanner message={pauseResume.error} />}

      <PauseConfirmDialog
        open={pauseResume.confirmPause}
        onOpenChange={pauseResume.setConfirmPause}
        name={name}
        busy={pauseResume.isPausing}
        onConfirm={() => void pauseResume.onPause()}
      />
    </div>
  )
}
