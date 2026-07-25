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
import { httpStatus } from '@/lib/rtk-error'
import type { Mailbox } from '@/store/api'
import type { StartOauthResponse } from './api'
import {
  useListMailboxesQuery,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useDeleteMailboxMutation,
  useStartGoogleOauthMutation,
  useStartMicrosoftOauthMutation,
} from './api'
import { mailboxTone, mailboxStatusLabel } from './status'
import { ConnectMailboxForm } from './connect-mailbox-form'
import { GoogleIcon } from './google-icon'
import { MicrosoftIcon } from './microsoft-icon'
import { mailboxProviderLabel } from './provider'
import { BannerShell, OauthCallbackBanner } from './oauth-callback-banner'
import {
  startErrorCopy,
  startErrorKind,
  type OauthProvider,
  type StartErrorKind,
} from './oauth-start-error'

// The awaited result of an RTK Query OAuth-start mutation trigger: either the
// consent-URL payload or a (typed) error. Both providers share this shape.
type OauthStartResult = { data?: StartOauthResponse } | { error?: unknown }

export function MailboxesPage() {
  const [showConnect, setShowConnect] = useState(false)
  // Track which provider's start failed so the banner shows provider-correct
  // copy from the single shared mapping.
  const [startError, setStartError] = useState<{ provider: OauthProvider; kind: StartErrorKind } | null>(null)
  const { data, isLoading } = useListMailboxesQuery()
  const [startGoogleOauth, { isLoading: startingGmail }] = useStartGoogleOauthMutation()
  const [startMicrosoftOauth, { isLoading: startingMicrosoft }] = useStartMicrosoftOauthMutation()
  const mailboxes = data ?? []

  const count = (s: string) => mailboxes.filter((m) => m.status === s).length

  // Shared one-click OAuth kickoff for every provider: fire the given start
  // mutation, and when the server hands back a consent URL, full-page redirect
  // the browser to it. Resolves `true` when a redirect is under way; on failure
  // it records the provider + error kind (501 = that provider's OAuth isn't
  // configured; anything else transient) and resolves `false` so the caller can
  // close the menu and reveal the banner.
  async function onConnectOAuth(
    provider: OauthProvider,
    start: () => Promise<OauthStartResult>,
  ): Promise<boolean> {
    setStartError(null)
    const result = await start()
    if ('data' in result && result.data?.auth_url) {
      window.location.assign(result.data.auth_url)
      return true
    }
    setStartError({ provider, kind: startErrorKind('error' in result ? result.error : undefined) })
    return false
  }

  const onConnectGmail = () => onConnectOAuth('gmail', () => startGoogleOauth())
  const onConnectMicrosoft = () => onConnectOAuth('microsoft', () => startMicrosoftOauth())

  return (
    <Page>
      <PageTopbar
        eyebrow="Mailboxes"
        actions={
          <ConnectMenu
            startingGmail={startingGmail}
            startingMicrosoft={startingMicrosoft}
            onGmail={onConnectGmail}
            onMicrosoft={onConnectMicrosoft}
            onSmtp={() => setShowConnect(true)}
          />
        }
      />

      <OauthCallbackBanner />

      {startError && (
        <BannerShell tone="danger" onDismiss={() => setStartError(null)}>
          <AlertCircle className="size-4 shrink-0 text-danger" aria-hidden="true" />
          <span className="min-w-0 flex-1">{startErrorCopy(startError.provider)[startError.kind]}</span>
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
            description="Connect a Gmail or Microsoft 365 account in one click, or an SMTP/IMAP mailbox with credentials, to start sending and warming. Credentials are encrypted at rest and verified before saving."
            action={
              <ConnectMenu
                startingGmail={startingGmail}
                startingMicrosoft={startingMicrosoft}
                onGmail={onConnectGmail}
                onMicrosoft={onConnectMicrosoft}
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
 * The "Connect mailbox" primary action, split by provider: Gmail or Microsoft
 * 365 (one-click OAuth) or SMTP/IMAP (the credentialled inline form). Same
 * trigger button is reused in the topbar and the empty state.
 */
function ConnectMenu({
  startingGmail,
  startingMicrosoft,
  onGmail,
  onMicrosoft,
  onSmtp,
  triggerLabel,
}: {
  startingGmail: boolean
  startingMicrosoft: boolean
  onGmail: () => Promise<boolean>
  onMicrosoft: () => Promise<boolean>
  onSmtp: () => void
  // Overrides the trigger's accessible name without changing the visible label.
  // The empty state renders a second identical trigger, so it passes a distinct
  // label ("Connect your first mailbox") to keep the two tellable apart by
  // screen readers.
  triggerLabel?: string
}) {
  // Own the menu's open state so we can keep it open while an OAuth request is
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
        <OauthMenuItem
          label="Gmail"
          icon={<GoogleIcon className="size-4" />}
          starting={startingGmail}
          onConnect={onGmail}
          onFail={() => setOpen(false)}
        />
        <OauthMenuItem
          label="Microsoft 365"
          icon={<MicrosoftIcon className="size-4" />}
          starting={startingMicrosoft}
          onConnect={onMicrosoft}
          onFail={() => setOpen(false)}
        />
        <DropdownMenuItem onSelect={() => onSmtp()}>
          <Mail className="size-4" />
          SMTP / IMAP
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * A single one-click-OAuth provider row in the ConnectMenu. Shared by every
 * OAuth provider so the pending spinner, the keep-open-during-pending /
 * close-on-failure dance, and the floating-promise guard live in exactly one
 * place. `onConnect` resolves `true` while a redirect is under way (menu stays
 * open) and `false` on failure (call `onFail` to close and reveal the banner).
 */
function OauthMenuItem({
  label,
  icon,
  starting,
  onConnect,
  onFail,
}: {
  label: string
  icon: React.ReactNode
  starting: boolean
  onConnect: () => Promise<boolean>
  onFail: () => void
}) {
  return (
    <DropdownMenuItem
      disabled={starting}
      onSelect={(e) => {
        // Keep the menu open during pending; on success the browser redirects
        // to the provider, on failure close it to reveal the banner.
        e.preventDefault()
        void (async () => {
          const redirecting = await onConnect()
          if (!redirecting) onFail()
        })()
      }}
    >
      {starting ? <Loader2 className="size-4 animate-spin" /> : icon}
      {label}
    </DropdownMenuItem>
  )
}

function MailboxRow({ mailbox }: { mailbox: Mailbox }) {
  const [pause, pauseState] = usePauseMailboxMutation()
  const [resume, resumeState] = useResumeMailboxMutation()
  const [remove, removeState] = useDeleteMailboxMutation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  // A failed pause/resume/delete was previously silent — surface it inline on
  // the row (the menu/dialog has already closed by the time this shows, so the
  // error isn't hidden underneath either).
  const [actionError, setActionError] = useState<string | null>(null)
  const id = mailbox.id ?? ''
  const busy = pauseState.isLoading || resumeState.isLoading || removeState.isLoading
  // Both OAuth providers send via a hosted API rather than SMTP; the row's
  // secondary line reads "<Provider> · API" for them (SMTP has no label here).
  const oauthLabel = mailboxProviderLabel(mailbox.provider)

  async function onPause() {
    setActionError(null)
    const res = await pause({ id })
    if ('error' in res) setActionError(mailboxActionErrorMessage('pause', httpStatus(res.error)))
  }
  async function onResume() {
    setActionError(null)
    const res = await resume({ id })
    if ('error' in res) setActionError(mailboxActionErrorMessage('resume', httpStatus(res.error)))
  }
  async function onDelete() {
    setActionError(null)
    const res = await remove({ id })
    setConfirmDelete(false)
    if ('error' in res) setActionError(mailboxActionErrorMessage('delete', httpStatus(res.error)))
  }

  return (
    <li className="flex items-center gap-4 border-b border-border px-5 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{mailbox.email}</span>
          {mailbox.display_name && <span className="truncate text-xs text-muted-foreground">{mailbox.display_name}</span>}
          <ProviderTag provider={mailbox.provider} />
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-faint">
          {oauthLabel ? `${oauthLabel} · API` : `${mailbox.smtp_host}:${mailbox.smtp_port}`}
          {mailbox.last_error ? <span className="text-danger"> · {mailbox.last_error}</span> : null}
        </div>
        {actionError && (
          <div role="alert" className="mt-0.5 text-[11px] text-danger">
            {actionError}
          </div>
        )}
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

/** Human copy for a failed row-level mailbox mutation, narrowed via httpStatus. */
function mailboxActionErrorMessage(action: 'pause' | 'resume' | 'delete', status?: number): string {
  if (status === 404) return 'This mailbox no longer exists — refresh the page.'
  return `Couldn't ${action} this mailbox. Please try again.`
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
 * Faint provider chip on a mailbox row. The text label ("Gmail"/"Microsoft
 * 365"/"SMTP") is the signal — the brand mark is only a reinforcing decoration,
 * so color is never the sole indicator.
 */
function ProviderTag({ provider }: { provider?: string }) {
  // Label comes from the single shared source; the icon is a reinforcing
  // decoration only, so an absent/unknown provider falls back to plain "SMTP".
  const label = mailboxProviderLabel(provider) ?? 'SMTP'
  const icon =
    provider === 'gmail' ? (
      <GoogleIcon className="size-3" />
    ) : provider === 'm365' ? (
      <MicrosoftIcon className="size-3" />
    ) : null
  return (
    <span className="flex shrink-0 items-center gap-1 rounded border border-border px-1.5 font-mono text-[10px] uppercase tracking-[0.08em] text-faint">
      {icon}
      {label}
    </span>
  )
}
