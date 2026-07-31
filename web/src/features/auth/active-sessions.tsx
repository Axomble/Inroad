import { useState } from 'react'
import { AlertCircle, Loader2, LogOut, Monitor, Smartphone, ShieldCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import type { SessionInfo } from '@/store/api'
import {
  useAuthListSessionsQuery,
  useAuthRevokeSessionMutation,
  useAuthRevokeOtherSessionsMutation,
} from './api'
import { describeUserAgent, relativeTime, formatDateTime } from './session-format'

/**
 * Security → Active sessions. Lists the caller's revocable sessions (P1 auth
 * hardening): the current session is badged and can't be revoked, every other
 * one can be signed out individually or all at once ("sign out everywhere
 * else"). Server state lives entirely in RTK Query; revoking invalidates the
 * `Sessions` list tag so the view refetches itself.
 */
export function ActiveSessions() {
  const { data, isLoading, isError, refetch } = useAuthListSessionsQuery()

  // One shared, transient surface for action outcomes (a revoke failure, or the
  // signed-out count) so the markup isn't duplicated across rows.
  const [notice, setNotice] = useState<Notice | null>(null)

  const sessions = [...(data?.sessions ?? [])].sort(sortCurrentFirst)
  const others = sessions.filter((s) => !s.current)

  return (
    <Page>
      <PageTopbar
        eyebrow="Security"
        title="Active sessions"
        subtitle="Devices signed in to your account"
        actions={
          <SignOutEverywhereElse
            disabled={isLoading || isError || others.length === 0}
            onDone={(revoked) =>
              setNotice({ tone: 'ok', text: `Signed out ${revoked} other session${revoked === 1 ? '' : 's'}.` })
            }
            onError={() => setNotice({ tone: 'error', text: "Couldn't sign out other sessions. Please try again." })}
          />
        }
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <ListError onRetry={() => void refetch()} />
        ) : (
          <>
            {sessions
              .filter((s) => s.current)
              .map((s) => (
                <SessionRow key={s.id} session={s} onError={setNotice} />
              ))}

            <SectionBar label="Other sessions" count={others.length} />
            {others.length === 0 ? (
              <EmptyBlock
                title="No other active sessions"
                description="You're only signed in on this device right now."
              />
            ) : (
              others.map((s) => <SessionRow key={s.id} session={s} onError={setNotice} />)
            )}
          </>
        )}
      </PageBody>
    </Page>
  )
}

type Notice = { tone: 'ok' | 'error'; text: string }

function sortCurrentFirst(a: SessionInfo, b: SessionInfo): number {
  if (a.current !== b.current) return a.current ? -1 : 1
  return new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
}

function isMobileAgent(userAgent: string | null | undefined): boolean {
  return !!userAgent && /\biPhone\b|\biPad\b|\biPod\b|\bAndroid\b|\bMobile\b/.test(userAgent)
}

function SessionRow({ session, onError }: { session: SessionInfo; onError: (n: Notice) => void }) {
  const [revoke, { isLoading }] = useAuthRevokeSessionMutation()
  const Icon = isMobileAgent(session.user_agent) ? Smartphone : Monitor
  const device = describeUserAgent(session.user_agent)

  async function onRevoke() {
    const result = await revoke({ id: session.id })
    if ('error' in result) {
      const status = httpStatus(result.error)
      onError({
        tone: 'error',
        text:
          status === 404
            ? 'That session is already signed out.'
            : "Couldn't revoke that session. Please try again.",
      })
    }
  }

  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-3.5">
      <Icon className="size-5 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{device}</span>
          {session.current && (
            // Badge, not colour alone — the label carries the meaning for
            // colourblind users and screen readers alike.
            <StatusPill tone="running">This device</StatusPill>
          )}
        </div>
        <div className="mt-0.5 font-mono text-[11px] text-faint">
          {session.ip ?? 'Unknown IP'} · started {formatDateTime(session.created_at)} · expires{' '}
          {relativeTime(session.expires_at)}
        </div>
      </div>

      {session.current ? (
        <span className="font-mono text-[11px] uppercase tracking-[0.1em] text-faint">Current</span>
      ) : (
        <Button
          variant="outline"
          size="sm"
          disabled={isLoading}
          aria-label={`Revoke session on ${device}`}
          onClick={() => void onRevoke()}
        >
          {isLoading && <Loader2 className="size-3.5 animate-spin" />}
          Revoke
        </Button>
      )}
    </div>
  )
}

function SignOutEverywhereElse({
  disabled,
  onDone,
  onError,
}: {
  disabled: boolean
  onDone: (revoked: number) => void
  onError: () => void
}) {
  const [open, setOpen] = useState(false)
  const [revokeOthers, { isLoading }] = useAuthRevokeOtherSessionsMutation()

  async function onConfirm() {
    const result = await revokeOthers()
    if ('data' in result && result.data) {
      onDone(result.data.revoked)
      setOpen(false)
    } else {
      onError()
      setOpen(false)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger asChild>
        <Button variant="destructive" size="sm" disabled={disabled}>
          <LogOut className="size-3.5" />
          Sign out everywhere else
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Sign out everywhere else?</AlertDialogTitle>
          <AlertDialogDescription>
            This revokes every other session and keeps you signed in only on this device. Signed-out devices
            will need to log in again.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isLoading}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="bg-danger text-destructive-foreground"
            disabled={isLoading}
            onClick={(e) => {
              // Keep the dialog mounted while the request runs; close on result.
              e.preventDefault()
              void onConfirm()
            }}
          >
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Sign out other sessions
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function NoticeBanner({ notice }: { notice: Notice }) {
  const isError = notice.tone === 'error'
  return (
    <div
      role={isError ? 'alert' : 'status'}
      className={
        isError
          ? 'flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-5 py-2.5 text-xs text-danger'
          : 'flex items-center gap-2 border-b border-ok/30 bg-ok/10 px-5 py-2.5 text-xs text-ok'
      }
    >
      {isError ? (
        <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
      ) : (
        <ShieldCheck className="size-3.5 shrink-0" aria-hidden="true" />
      )}
      {notice.text}
    </div>
  )
}

function ListError({ onRetry }: { onRetry: () => void }) {
  return (
    <EmptyBlock
      title="Couldn't load your sessions"
      description="Something went wrong fetching your active sessions. Please try again."
      action={
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      }
    />
  )
}

function LoadingRows() {
  return (
    <div>
      {[0, 1].map((i) => (
        <div key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <Skeleton className="size-5 rounded-md" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-44" />
            <Skeleton className="h-2.5 w-64" />
          </div>
          <Skeleton className="h-8 w-20" />
        </div>
      ))}
    </div>
  )
}
