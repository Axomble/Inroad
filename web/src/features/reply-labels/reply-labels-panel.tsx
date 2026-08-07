import { Suspense, lazy, useState } from 'react'
import { Loader2, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { NoticeBanner, type Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { useListReplyLabelsQuery, useDeleteReplyLabelMutation, type ReplyLabel } from './api'
import { LabelRowBody } from './label-row'
import { ReplyLabelDialog } from './reply-label-dialog'
import { replyLabelErrorMessage } from './error-messages'

const SortableLabelList = lazy(() => import('./sortable-label-list'))

/**
 * Settings → Reply labels. The workspace's reply taxonomy: what the classifier
 * may label a reply as, and what each label does to the enrollment that got it.
 * Writes are campaigns:write on the backend — a scope every logged-in member
 * implicitly holds (sessions pass all scope checks; only API keys / OAuth
 * grants are attenuated) — so unlike API keys there is no admin gate here.
 * Server state lives in RTK Query; every mutation invalidates the `ReplyLabel`
 * LIST tag so the view refetches itself.
 */
export function ReplyLabelsPanel() {
  const { data, isLoading, isError, refetch } = useListReplyLabelsQuery()
  const [notice, setNotice] = useState<Notice | null>(null)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<ReplyLabel | null>(null)
  const [deleting, setDeleting] = useState<ReplyLabel | null>(null)

  const labels = data?.labels ?? []

  return (
    <Page>
      <PageTopbar
        eyebrow="Workspace"
        title="Reply labels"
        subtitle="What a reply can be classified as, and what each label does to the enrollment"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New label
          </Button>
        }
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <EmptyBlock
            title="Couldn't load reply labels"
            description="Something went wrong fetching this workspace's reply labels. Please try again."
            action={
              <Button variant="outline" size="sm" onClick={() => void refetch()}>
                Retry
              </Button>
            }
          />
        ) : labels.length === 0 ? (
          <EmptyBlock
            title="No reply labels"
            description="Create a label to classify replies and drive what happens to the enrollment that received them."
            action={
              <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
                <Plus className="size-4" />
                Create your first label
              </Button>
            }
          />
        ) : (
          <>
            <SectionBar label="Labels" count={labels.length} />
            <Suspense fallback={<StaticLabelList labels={labels} onEdit={setEditing} onDelete={setDeleting} />}>
              <SortableLabelList
                labels={labels}
                onEdit={setEditing}
                onDelete={setDeleting}
                onReorderError={(message) => setNotice(message ? { tone: 'error', text: message } : null)}
                refetch={() => void refetch()}
              />
            </Suspense>
          </>
        )}
      </PageBody>

      {creating && <ReplyLabelDialog onClose={() => setCreating(false)} onNotice={setNotice} />}
      {editing && <ReplyLabelDialog initial={editing} onClose={() => setEditing(null)} onNotice={setNotice} />}
      {deleting && (
        <DeleteLabelDialog label={deleting} onClose={() => setDeleting(null)} onNotice={setNotice} />
      )}
    </Page>
  )
}

/** The rows without dnd — what renders while the sortable chunk downloads. */
function StaticLabelList({
  labels,
  onEdit,
  onDelete,
}: {
  labels: ReplyLabel[]
  onEdit: (label: ReplyLabel) => void
  onDelete: (label: ReplyLabel) => void
}) {
  return (
    <div>
      {labels.map((label) => (
        <LabelRowBody key={label.id} label={label} onEdit={() => onEdit(label)} onDelete={() => onDelete(label)} />
      ))}
    </div>
  )
}

function DeleteLabelDialog({
  label,
  onClose,
  onNotice,
}: {
  label: ReplyLabel
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  const [deleteLabel, { isLoading }] = useDeleteReplyLabelMutation()

  async function onConfirm() {
    const result = await deleteLabel({ id: label.id })
    // Close first so the outcome banner isn't hidden under the dialog.
    onClose()
    if ('error' in result) {
      onNotice({ tone: 'error', text: replyLabelErrorMessage('delete', result.error) })
    } else {
      onNotice({ tone: 'ok', text: `Label “${label.label}” was deleted.` })
    }
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isLoading && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete “{label.label}”?</AlertDialogTitle>
          <AlertDialogDescription>
            New replies can no longer receive this label. Replies already classified with it keep the key{' '}
            <code className="font-mono">{label.key}</code> — a recorded classification is a fact, not a link.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onConfirm()}>
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Delete label
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function LoadingRows() {
  return (
    <div>
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex items-center gap-3 border-b border-border px-5 py-3">
          <Skeleton className="size-3.5 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-32" />
          </div>
          <Skeleton className="h-8 w-16" />
          <Skeleton className="h-8 w-20" />
        </div>
      ))}
    </div>
  )
}
