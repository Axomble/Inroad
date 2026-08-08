import { useState } from 'react'
import { AlertCircle, Flame, Loader2, Mail, MoreVertical, Plus } from 'lucide-react'
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
import { StatusPill, StatusDot } from '@/components/shared/status-pill'
import { HealthBadge } from '@/components/shared/health-badge'
import { ListSearchInput } from '@/components/shared/list-search-input'
import { SortMenu } from '@/components/shared/sort-menu'
import {
  Page,
  PageTopbar,
  StatStrip,
  Stat,
  SectionBar,
  PageBody,
  EmptyBlock,
  ListHeader,
  ListHeaderCell,
  HintBar,
} from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useListControls, byText, byRank, type SortOption } from '@/hooks/use-list-controls'
import { useListKeyboardNav, LIST_NAV_HINTS_NO_OPEN } from '@/hooks/use-list-keyboard-nav'
// Read-only cross-feature query-hook reuse is the established pattern here (see
// features/warmup/warmup-page.tsx pulling the mailbox list); cross-feature *UI*
// imports are not, which is why the warmup badge now lives in components/shared.
import { useGetWarmupOverviewQuery } from '@/features/warmup/api'
import type { Mailbox, WarmupMailbox } from '@/store/api'
import type { StartOauthResponse } from './api'
import {
  useListMailboxesQuery,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useDeleteMailboxMutation,
  useStartGoogleOauthMutation,
  useStartMicrosoftOauthMutation,
  useListSendingDomainsQuery,
} from './api'
import { mailboxTone, mailboxStatusLabel } from './status'
import { ConnectMailboxForm } from './connect-mailbox-form'
import { GoogleIcon } from './google-icon'
import { MicrosoftIcon } from './microsoft-icon'
import { mailboxProviderLabel } from './provider'
import { BannerShell, OauthCallbackBanner } from './oauth-callback-banner'
import { DomainAuthHeader, DomainAuthNotice } from './domain-auth-header'
import { domainGroupLabel, groupMailboxesByDomain } from './domain-group'
import {
  startErrorCopy,
  startErrorKind,
  type OauthProvider,
  type StartErrorKind,
} from './oauth-start-error'

// The awaited result of an RTK Query OAuth-start mutation trigger: either the
// consent-URL payload or a (typed) error. Both providers share this shape.
type OauthStartResult = { data?: StartOauthResponse } | { error?: unknown }

/**
 * Module scope so `useListControls` can memoise on comparator identity.
 *
 * "Needs attention" leads because this page answers one question — can I send
 * from these today? — so errored and paused mailboxes belong at the top.
 */
const SORTS: readonly SortOption<Mailbox>[] = [
  {
    id: 'attention',
    label: 'Needs attention',
    compare: byRank((m) => m.status, ['error', 'paused', 'active']),
  },
  { id: 'email', label: 'Email', compare: byText((m) => m.email) },
  { id: 'provider', label: 'Provider', compare: byText((m) => m.provider) },
]

export function MailboxesPage() {
  const [showConnect, setShowConnect] = useState(false)
  // Track which provider's start failed so the banner shows provider-correct
  // copy from the single shared mapping.
  const [startError, setStartError] = useState<{ provider: OauthProvider; kind: StartErrorKind } | null>(null)
  const { data, isLoading, error: listError, refetch } = useListMailboxesQuery()
  // Domain authentication is read here rather than in a section of its own: the
  // verdict belongs on the domain heading above the mailboxes it governs, so a
  // workspace with ten domains costs ten lines instead of ten stacked blocks.
  const { data: domains, isLoading: isLoadingDomains, error: domainsError } = useListSendingDomainsQuery()
  // Warmup state belongs on the mailbox row: the mailbox is the unit of trust,
  // so its identity, sending status, and reputation must be answerable on one
  // screen instead of forcing a page switch to /app/warmup. Read-only, and the
  // warmup page shares the same cache entry.
  const { data: warmup } = useGetWarmupOverviewQuery()
  const [startGoogleOauth, { isLoading: startingGmail }] = useStartGoogleOauthMutation()
  const [startMicrosoftOauth, { isLoading: startingMicrosoft }] = useStartMicrosoftOauthMutation()
  const mailboxes = data ?? []

  // Plain construction, no `useMemo`: nothing downstream depends on this Map's
  // identity (rows read through `.get()`), and building it is one pass over a
  // few dozen entries. Memoizing would buy nothing but a dependency array.
  const warmupByMailbox = new Map((warmup?.mailboxes ?? []).map((entry) => [entry.mailbox_id, entry]))

  const controls = useListControls({
    items: mailboxes,
    searchFields: (m) => [m.email, m.display_name, m.provider, m.smtp_host],
    sorts: SORTS,
  })

  // Grouping never drops a mailbox, so the flat row count keyboard nav walks is
  // still the filtered list's length; only the visual order changes.
  const groups = groupMailboxesByDomain(controls.items, domains ?? [])
  const nav = useListKeyboardNav({ count: controls.items.length })

  const count = (s: string) => mailboxes.filter((m) => m.status === s).length
  const isEmpty = mailboxes.length === 0

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
        <Stat label="Total" value={listError ? '\u2014' : mailboxes.length} sub="connected" />
        <Stat
          label="Active"
          value={listError ? '\u2014' : count('active')}
          dot={<StatusDot tone="running" />}
          sub="able to send"
        />
        <Stat
          label="Paused"
          value={listError ? '\u2014' : count('paused')}
          dot={<StatusDot tone="paused" />}
          sub="resumable"
        />
        <Stat
          label="Error"
          value={listError ? '\u2014' : count('error')}
          dot={<StatusDot tone="failing" />}
          sub="needs attention"
        />
      </StatStrip>

      {showConnect && (
        <ConnectMailboxForm onDone={() => setShowConnect(false)} onCancel={() => setShowConnect(false)} />
      )}

      {/* A failed domains load can't be told on the group headings themselves,
          so it says so once here rather than reading as "nothing to fix". */}
      {!isEmpty && domainsError && <DomainAuthNotice error={domainsError} />}

      {!isEmpty && (
        <SectionBar
          label="Mailboxes"
          count={controls.isFiltered ? `${controls.items.length}/${controls.totalCount}` : controls.totalCount}
        >
          <ListSearchInput
            value={controls.query}
            onChange={controls.setQuery}
            placeholder="Search by email…"
          />
          <SortMenu options={SORTS} value={controls.sortId} onChange={controls.setSortId} />
        </SectionBar>
      )}

      {isLoading ? (
        <PageBody>
          <LoadingRows />
        </PageBody>
      ) : listError ? (
        <PageBody>
          <EmptyBlock
            title="Couldn't load mailboxes"
            description={`Your mailbox data is safe, but the server couldn't return it${httpStatus(listError) ? ` (${httpStatus(listError)})` : ''}. Check the connection and try again.`}
            action={
              <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                Try again
              </Button>
            }
          />
        </PageBody>
      ) : isEmpty ? (
        <PageBody>
          {!showConnect && (
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
          )}
        </PageBody>
      ) : (
        <>
          <ListHeader>
            <ListHeaderCell className="min-w-0 flex-1">Mailbox</ListHeaderCell>
            <ListHeaderCell className="hidden w-40 xl:block">Warmup</ListHeaderCell>
            <ListHeaderCell className="hidden w-16 text-right lg:block">Cap</ListHeaderCell>
            <ListHeaderCell className="w-20 text-right">Status</ListHeaderCell>
            <ListHeaderCell className="w-8" aria-label="Actions" />
          </ListHeader>

          <PageBody ref={nav.containerRef}>
            {controls.items.length === 0 ? (
              <EmptyBlock
                title="No mailboxes match this search"
                description={`Nothing matches "${controls.query}". Clear the search to see all ${controls.totalCount} mailboxes.`}
                action={
                  <Button variant="secondary" size="sm" onClick={controls.clear}>
                    Clear search
                  </Button>
                }
              />
            ) : (
              <ul>
                {groups.map((group) => (
                  <li key={group.domain}>
                    <DomainAuthHeader group={group} isLoadingAuth={isLoadingDomains} />
                    <ul aria-label={`Mailboxes on ${domainGroupLabel(group)}`}>
                      {group.mailboxes.map((m, offset) => {
                        const index = group.startIndex + offset
                        return (
                          <MailboxRow
                            key={m.id}
                            mailbox={m}
                            warmup={warmupByMailbox.get(m.id ?? '')}
                            index={index}
                            active={nav.isActive(index)}
                            onHover={nav.onRowHover}
                          />
                        )
                      })}
                    </ul>
                  </li>
                ))}
              </ul>
            )}
          </PageBody>

          {/* No `↵ open` — mailbox rows have no detail view to open. */}
          <HintBar hints={LIST_NAV_HINTS_NO_OPEN} />
        </>
      )}
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

function MailboxRow({
  mailbox,
  warmup,
  index,
  active,
  onHover,
}: {
  mailbox: Mailbox
  /** This mailbox's warmup row, present only when it's enrolled in the pool. */
  warmup?: WarmupMailbox
  index: number
  active: boolean
  onHover: (index: number) => void
}) {
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
    <li
      data-row-index={index}
      onMouseEnter={() => onHover(index)}
      className={cn(
        'flex items-center gap-3 border-b border-border px-4 py-3 transition-colors sm:gap-4 sm:px-5',
        active && 'bg-surface-2/60',
      )}
    >
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

      <div className="hidden w-40 shrink-0 xl:block">
        <WarmupCell entry={warmup} />
      </div>

      <div className="hidden w-16 shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground lg:block">
        {mailbox.daily_cap}/day
      </div>

      <div className="flex w-20 shrink-0 justify-end">
        <StatusPill tone={mailboxTone(mailbox.status)}>{mailboxStatusLabel(mailbox.status)}</StatusPill>
      </div>

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

/**
 * A mailbox's warmup state, inline on its own row: reputation health plus today's
 * ramp progress, or an explicit "off" when it isn't in the pool.
 *
 * Read-only by design. Enabling, disabling, and tuning warmup stay on
 * `/app/warmup`, which owns those mutations — this cell exists so the question
 * "can I trust this mailbox to send today?" is answerable without leaving the
 * page, not to duplicate the warmup feature's controls here.
 */
function WarmupCell({ entry }: { entry?: WarmupMailbox }) {
  if (!entry?.enabled) {
    return <span className="font-mono text-[10.5px] uppercase tracking-[0.1em] text-faint">Off</span>
  }
  return (
    <div className="flex min-w-0 items-center gap-2">
      <HealthBadge state={entry.health_state} reason={entry.health_reason} />
      <span className="flex items-center gap-1 font-mono text-[11px] tabular-nums text-muted-foreground">
        <Flame className="size-3 text-warm" aria-hidden="true" />
        {entry.today_sent}/{entry.today_target}
      </span>
    </div>
  )
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
