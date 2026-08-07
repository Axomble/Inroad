import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, Pencil, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { httpStatus } from '@/lib/rtk-error'
import { useRenameListMutation, useDeleteListMutation } from './api'

/**
 * Hover-revealed rename/delete pair for a list row in the contacts sidebar,
 * following the row-action precedent in features/agent/history.tsx (icons that
 * appear on the row's hover/focus so the narrow sidebar stays quiet). The
 * parent row must carry the `group` class.
 */
export function ListRowActions({
  id,
  name,
  onDeleted,
}: {
  id: string
  name: string
  /** Fired after a confirmed delete succeeds, before the list refetches. */
  onDeleted: () => void
}) {
  const [renaming, setRenaming] = useState(false)
  const [deleting, setDeleting] = useState(false)

  return (
    <>
      <div className="mr-1 flex shrink-0 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
        <button
          type="button"
          className="rounded p-1 text-faint hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Rename list ${name}`}
          onClick={() => setRenaming(true)}
        >
          <Pencil className="size-3" />
        </button>
        <button
          type="button"
          className="rounded p-1 text-faint hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Delete list ${name}`}
          onClick={() => setDeleting(true)}
        >
          <Trash2 className="size-3" />
        </button>
      </div>

      {renaming && <RenameListDialog id={id} name={name} onClose={() => setRenaming(false)} />}
      {deleting && <DeleteListDialog id={id} name={name} onClose={() => setDeleting(false)} onDeleted={onDeleted} />}
    </>
  )
}

// Mirrors the backend's rename validation (internal/app/list/handler.go:
// required, 1–200 chars) so an over-long name is explained here, not as a 400.
const renameSchema = z.object({
  name: z.string().trim().min(1, 'Name the list').max(200, 'Keep the name to 200 characters'),
})
type RenameValues = z.infer<typeof renameSchema>

function RenameListDialog({ id, name, onClose }: { id: string; name: string; onClose: () => void }) {
  const nameId = useId()
  const [rename, { isLoading }] = useRenameListMutation()
  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<RenameValues>({
    resolver: zodResolver(renameSchema),
    defaultValues: { name },
  })

  const submit = handleSubmit(async (values) => {
    const result = await rename({ id, body: { name: values.name.trim() } })
    if ('error' in result) {
      setError('root', {
        message:
          httpStatus(result.error) === 404
            ? 'That list no longer exists — it may have been deleted.'
            : "Couldn't rename the list. Please try again.",
      })
      return
    }
    onClose()
  })

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isLoading && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Rename “{name}”</AlertDialogTitle>
          <AlertDialogDescription>
            Campaigns targeting this list keep working — only the display name changes.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <div>
            <Label htmlFor={nameId}>Name</Label>
            <Input id={nameId} className="mt-1.5" autoFocus aria-invalid={!!errors.name} {...register('name')} />
            {errors.name && (
              <p role="alert" className="mt-1 text-xs text-danger">
                {errors.name.message}
              </p>
            )}
          </div>

          {errors.root?.message && (
            <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
              {errors.root.message}
            </p>
          )}

          <AlertDialogFooter>
            <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              Rename list
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteListDialog({
  id,
  name,
  onClose,
  onDeleted,
}: {
  id: string
  name: string
  onClose: () => void
  onDeleted: () => void
}) {
  const [deleteList, { isLoading }] = useDeleteListMutation()
  const [error, setErrorText] = useState<string | null>(null)

  async function onConfirm() {
    setErrorText(null)
    const result = await deleteList({ id })
    if ('error' in result) {
      const status = httpStatus(result.error)
      // The 409 is the one outcome the user can act on, so it gets its own
      // explanation instead of the generic failure line.
      setErrorText(
        status === 409
          ? 'This list is used by a campaign — point the campaign at another list before deleting it.'
          : status === 404
            ? 'That list was already deleted.'
            : "Couldn't delete the list. Please try again.",
      )
      return
    }
    onDeleted()
    onClose()
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isLoading && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete “{name}”?</AlertDialogTitle>
          <AlertDialogDescription>
            The list and its memberships are removed. The contacts themselves stay in the workspace.
          </AlertDialogDescription>
        </AlertDialogHeader>

        {error && (
          <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {error}
          </p>
        )}

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onConfirm()}>
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Delete list
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
