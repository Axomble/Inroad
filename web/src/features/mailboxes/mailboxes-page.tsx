import { useState } from 'react'
import { MoreVertical, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
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
import { StatusPill } from '@/components/shared/status-pill'
import { Page, PageTopbar, StatStrip, Stat, PageBody, EmptyBlock } from '@/components/layout/page'
import type { Mailbox } from '@/store/api'
import {
  useListMailboxesQuery,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useDeleteMailboxMutation,
} from './api'
import { mailboxTone, mailboxStatusLabel } from './status'
import { ConnectMailboxForm } from './connect-mailbox-form'

export function MailboxesPage() {
  const [showConnect, setShowConnect] = useState(false)
  const { data, isLoading } = useListMailboxesQuery()
  const mailboxes = data ?? []

  const count = (s: string) => mailboxes.filter((m) => m.status === s).length

  return (
    <Page>
      <PageTopbar
        eyebrow="Mailboxes"
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowConnect((v) => !v)}>
            <Plus className="size-4" />
            Connect mailbox
          </Button>
        }
      />

      <StatStrip>
        <Stat label="Total" value={mailboxes.length} />
        <Stat label="Active" value={count('active')} dot={<Dot className="bg-ok" />} />
        <Stat label="Paused" value={count('paused')} dot={<Dot className="bg-warn" />} />
        <Stat label="Error" value={count('error')} dot={<Dot className="bg-danger" />} />
      </StatStrip>

      <PageBody>
        {showConnect && (
          <ConnectMailboxForm
            onDone={() => setShowConnect(false)}
            onCancel={() => setShowConnect(false)}
          />
        )}

        {isLoading ? (
          <LoadingRows />
        ) : mailboxes.length === 0 && !showConnect ? (
          <EmptyBlock
            title="No mailboxes connected"
            description="Connect an SMTP/IMAP mailbox to start sending and warming. Its credentials are encrypted at rest and verified before saving."
            action={
              <Button variant="primary" size="sm" onClick={() => setShowConnect(true)}>
                <Plus className="size-4" />
                Connect mailbox
              </Button>
            }
          />
        ) : (
          <ul>
            {mailboxes.map((m) => (
              <MailboxRow key={m.id} mailbox={m} />
            ))}
          </ul>
        )}
      </PageBody>
    </Page>
  )
}

function MailboxRow({ mailbox }: { mailbox: Mailbox }) {
  const [pause, pauseState] = usePauseMailboxMutation()
  const [resume, resumeState] = useResumeMailboxMutation()
  const [remove, removeState] = useDeleteMailboxMutation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const id = mailbox.id ?? ''
  const busy = pauseState.isLoading || resumeState.isLoading || removeState.isLoading

  async function onPause() {
    await pause({ id })
  }
  async function onResume() {
    await resume({ id })
  }
  async function onDelete() {
    await remove({ id })
    setConfirmDelete(false)
  }

  return (
    <li className="flex items-center gap-4 border-b border-border px-5 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{mailbox.email}</span>
          {mailbox.display_name && <span className="truncate text-xs text-muted-foreground">{mailbox.display_name}</span>}
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-faint">
          {mailbox.smtp_host}:{mailbox.smtp_port}
          {mailbox.last_error ? <span className="text-danger"> · {mailbox.last_error}</span> : null}
        </div>
      </div>

      <div className="flex items-center gap-2 tabular-nums">
        <span className="font-mono text-[11px] text-muted-foreground">{mailbox.daily_cap}/day</span>
      </div>

      <StatusPill tone={mailboxTone(mailbox.status)}>{mailboxStatusLabel(mailbox.status)}</StatusPill>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${mailbox.email}`}>
            <MoreVertical className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {mailbox.status === 'paused' ? (
            <DropdownMenuItem disabled={busy} onClick={onResume}>
              Resume
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem disabled={busy} onClick={onPause}>
              Pause
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            className="text-danger"
            disabled={busy}
            onSelect={(e) => {
              e.preventDefault()
              setConfirmDelete(true)
            }}
          >
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this mailbox?</AlertDialogTitle>
            <AlertDialogDescription>
              {mailbox.email} will be disconnected. Any in-flight sends from this mailbox will fail.
              This cannot be undone.
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
              Delete mailbox
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-32" />
          </div>
          <Skeleton className="h-4 w-16" />
        </li>
      ))}
    </ul>
  )
}

function Dot({ className }: { className?: string }) {
  return <span className={cn('size-1.5 rounded-full', className)} aria-hidden="true" />
}
