import { useState } from 'react'
import { AlertCircle, Loader2, Mail, MoreVertical, Plus } from 'lucide-react'
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
  useStartGoogleOauthMutation,
} from './api'
import { mailboxTone, mailboxStatusLabel } from './status'
import { ConnectMailboxForm } from './connect-mailbox-form'
import { GoogleIcon } from './google-icon'
import { BannerShell, OauthCallbackBanner } from './oauth-callback-banner'
import { startErrorCopy, startErrorKind, type StartErrorKind } from './oauth-start-error'

export function MailboxesPage() {
  const [showConnect, setShowConnect] = useState(false)
  const [startError, setStartError] = useState<StartErrorKind | null>(null)
  const { data, isLoading } = useListMailboxesQuery()
  const [startGoogleOauth, { isLoading: starting }] = useStartGoogleOauthMutation()
  const mailboxes = data ?? []

  const count = (s: string) => mailboxes.filter((m) => m.status === s).length

  // Kick off the Gmail OAuth flow: the server hands back a Google consent URL
  // and we full-page redirect the browser to it. Resolves `true` when a
  // redirect is under way; on failure it records the error kind (501 = Gmail
  // OAuth not configured; anything else transient) and resolves `false` so the
  // caller can close the menu and reveal the banner.
  async function onConnectGmail(): Promise<boolean> {
    setStartError(null)
    const result = await startGoogleOauth()
    if ('data' in result && result.data?.auth_url) {
      window.location.assign(result.data.auth_url)
      return true
    }
    setStartError(startErrorKind('error' in result ? result.error : undefined))
    return false
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Mailboxes"
        actions={
          <ConnectMenu
            starting={starting}
            onGmail={onConnectGmail}
            onSmtp={() => setShowConnect(true)}
          />
        }
      />

      <OauthCallbackBanner />

      {startError && (
        <BannerShell tone="danger" onDismiss={() => setStartError(null)}>
          <AlertCircle className="size-4 shrink-0 text-danger" aria-hidden="true" />
          <span className="min-w-0 flex-1">{startErrorCopy[startError]}</span>
        </BannerShell>
      )}

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
            description="Connect a Gmail account in one click, or an SMTP/IMAP mailbox with credentials, to start sending and warming. Credentials are encrypted at rest and verified before saving."
            action={
              <ConnectMenu
                starting={starting}
                onGmail={onConnectGmail}
                onSmtp={() => setShowConnect(true)}
                triggerLabel="Connect your first mailbox"
              />
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

/**
 * The "Connect mailbox" primary action, split by provider: Gmail (one-click
 * OAuth) or SMTP/IMAP (the credentialled inline form). Same trigger button is
 * reused in the topbar and the empty state.
 */
function ConnectMenu({
  starting,
  onGmail,
  onSmtp,
  triggerLabel,
}: {
  starting: boolean
  onGmail: () => Promise<boolean>
  onSmtp: () => void
  // Overrides the trigger's accessible name without changing the visible label.
  // The empty state renders a second identical trigger, so it passes a distinct
  // label ("Connect your first mailbox") to keep the two tellable apart by
  // screen readers.
  triggerLabel?: string
}) {
  // Own the menu's open state so we can keep it open while the Gmail request is
  // in flight but close it the moment that request fails — otherwise the Radix
  // menu stays open (onSelect is prevented) and covers the full-width error
  // banner that renders underneath it.
  const [open, setOpen] = useState(false)
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button variant="primary" size="sm" aria-label={triggerLabel}>
          <Plus className="size-4" />
          Connect mailbox
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem
          disabled={starting}
          onSelect={(e) => {
            // Keep the menu open during pending; on success the browser
            // redirects to Google, on failure close it to reveal the banner.
            e.preventDefault()
            void onGmail().then((redirecting) => {
              if (!redirecting) setOpen(false)
            })
          }}
        >
          {starting ? <Loader2 className="size-4 animate-spin" /> : <GoogleIcon className="size-4" />}
          Gmail
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSmtp()}>
          <Mail className="size-4" />
          SMTP / IMAP
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function MailboxRow({ mailbox }: { mailbox: Mailbox }) {
  const [pause, pauseState] = usePauseMailboxMutation()
  const [resume, resumeState] = useResumeMailboxMutation()
  const [remove, removeState] = useDeleteMailboxMutation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const id = mailbox.id ?? ''
  const busy = pauseState.isLoading || resumeState.isLoading || removeState.isLoading
  const isGmail = mailbox.provider === 'gmail'

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
          <ProviderTag gmail={isGmail} />
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-faint">
          {isGmail ? 'Gmail · API' : `${mailbox.smtp_host}:${mailbox.smtp_port}`}
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

/**
 * Faint provider chip on a mailbox row. The text label ("Gmail"/"SMTP") is the
 * signal — the Google mark is only a reinforcing decoration, so color is never
 * the sole indicator.
 */
function ProviderTag({ gmail }: { gmail: boolean }) {
  return (
    <span className="flex shrink-0 items-center gap-1 rounded border border-border px-1.5 font-mono text-[10px] uppercase tracking-[0.08em] text-faint">
      {gmail && <GoogleIcon className="size-3" />}
      {gmail ? 'Gmail' : 'SMTP'}
    </span>
  )
}
