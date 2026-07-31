import { useEffect, useId, useState } from 'react'
import {
  AlertCircle,
  Check,
  Copy,
  Download,
  KeyRound,
  Loader2,
  ShieldCheck,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { PageTopbar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import type { TwoFactorEnrollResponse } from '@/store/api'
import { QrCode } from './qr-code'
import {
  useAuthTwoFactorStatusQuery,
  useAuthTwoFactorEnrollMutation,
  useAuthTwoFactorConfirmMutation,
  useAuthTwoFactorDisableMutation,
} from './api'

/**
 * Security → Two-factor authentication (P2 auth hardening). Reads the caller's
 * TOTP status and drives the enroll / disable flows. Server state lives entirely
 * in RTK Query: enabling (confirm) and disabling invalidate the `TwoFactor`
 * status tag, so this panel refetches itself without any hand-rolled refetch.
 */
export function TwoFactorSettings() {
  const { data, isLoading, isError, refetch } = useAuthTwoFactorStatusQuery()
  const [notice, setNotice] = useState<Notice | null>(null)
  // The active dialog is owned here, at the top of the panel — deliberately NOT
  // inside the status row. Confirming enrollment flips `totp_enabled`, which
  // swaps the disabled row for the enabled one; if the dialog lived in the row
  // it would unmount mid-flow and the one-time recovery codes would vanish
  // before the user could save them. Held here, it survives that transition.
  const [dialog, setDialog] = useState<'enroll' | 'disable' | null>(null)

  return (
    <div className="flex flex-col">
      <PageTopbar
        eyebrow="Security"
        title="Two-factor authentication"
        subtitle="A second factor at sign-in"
      />

      {notice && <NoticeBanner notice={notice} />}

      {isLoading ? (
        <LoadingRow />
      ) : isError ? (
        <StatusError onRetry={() => void refetch()} />
      ) : data?.totp_enabled ? (
        <EnabledRow
          recoveryCodesRemaining={data.recovery_codes_remaining}
          onDisable={() => setDialog('disable')}
        />
      ) : (
        <DisabledRow onEnable={() => setDialog('enroll')} />
      )}

      {dialog === 'enroll' && (
        <EnrollDialog
          onClose={() => setDialog(null)}
          onEnabled={() => {
            setDialog(null)
            setNotice({ tone: 'ok', text: 'Two-factor authentication is on.' })
          }}
        />
      )}
      {dialog === 'disable' && (
        <DisableDialog
          onClose={() => setDialog(null)}
          onDisabled={() => {
            setDialog(null)
            setNotice({ tone: 'ok', text: 'Two-factor authentication is off. Other sessions were signed out.' })
          }}
        />
      )}
    </div>
  )
}

type Notice = { tone: 'ok' | 'error'; text: string }

function DisabledRow({ onEnable }: { onEnable: () => void }) {
  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-4">
      <KeyRound className="size-5 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13.5px] font-medium text-foreground">Authenticator app</span>
          <StatusPill tone="draft">Not enabled</StatusPill>
        </div>
        <p className="mt-0.5 text-[12px] text-muted-foreground">
          Add a time-based one-time code from an authenticator app to your sign-in.
        </p>
      </div>
      <Button variant="primary" size="sm" onClick={onEnable}>
        Enable 2FA
      </Button>
    </div>
  )
}

function EnabledRow({
  recoveryCodesRemaining,
  onDisable,
}: {
  recoveryCodesRemaining: number
  onDisable: () => void
}) {
  const low = recoveryCodesRemaining <= 3

  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-4">
      <ShieldCheck className="size-5 shrink-0 text-ok" strokeWidth={1.75} aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13.5px] font-medium text-foreground">Authenticator app</span>
          <StatusPill tone="running">Enabled</StatusPill>
        </div>
        <p className="mt-0.5 text-[12px] text-muted-foreground">
          <span className={low ? 'text-warn' : undefined}>
            {recoveryCodesRemaining} recovery code{recoveryCodesRemaining === 1 ? '' : 's'} remaining
          </span>
          {low && ' — regenerate soon by turning 2FA off and on again.'}
        </p>
      </div>
      <Button variant="outline" size="sm" onClick={onDisable}>
        Disable
      </Button>
    </div>
  )
}

/**
 * Two-step enrollment: scan the QR / enter the secret and confirm a code, then
 * see the one-time recovery codes. The codes are shown exactly once — the
 * "recovery" step is gated behind an explicit "I've saved these" acknowledgement
 * before it can be dismissed.
 */
function EnrollDialog({ onClose, onEnabled }: { onClose: () => void; onEnabled: () => void }) {
  const codeId = useId()
  const [enroll, { data: enrollData, isLoading: isEnrolling, error: enrollError }] =
    useAuthTwoFactorEnrollMutation()
  const [confirm, { isLoading: isConfirming }] = useAuthTwoFactorConfirmMutation()

  const [code, setCode] = useState('')
  const [confirmError, setConfirmError] = useState<string | null>(null)
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)

  // Begin enrollment once when the dialog mounts — stage the pending (unconfirmed)
  // secret so the QR + setup key can render.
  useEffect(() => {
    void enroll()
  }, [enroll])

  async function onConfirm() {
    setConfirmError(null)
    const result = await confirm({ twoFactorCodeRequest: { code: code.trim() } })
    if ('data' in result && result.data) {
      setRecoveryCodes(result.data.recovery_codes)
    } else {
      const status = httpStatus(result.error)
      setConfirmError(
        status === 400 || status === 401
          ? "That code didn't match. Check your authenticator app and try again."
          : "Something went wrong. Please try again.",
      )
    }
  }

  const onRecovery = recoveryCodes !== null

  return (
    <AlertDialog
      open
      onOpenChange={(next) => {
        // The recovery step must be acknowledged (RecoveryCodes calls onEnabled)
        // — ignore incidental close attempts (Escape) while it's showing.
        if (!next && !onRecovery) onClose()
      }}
    >
      <AlertDialogContent>
        {onRecovery ? (
          <RecoveryCodes codes={recoveryCodes} onDone={onEnabled} />
        ) : (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Set up authenticator app</AlertDialogTitle>
              <AlertDialogDescription>
                Scan the QR code with your authenticator app, or enter the setup key by hand. Then enter the
                6-digit code it shows.
              </AlertDialogDescription>
            </AlertDialogHeader>

            {isEnrolling || !enrollData ? (
              enrollError ? (
                <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
                  Couldn't start setup. Please close this and try again.
                </p>
              ) : (
                <div className="flex flex-col items-center gap-4 py-2">
                  <Skeleton className="size-52 rounded-lg" />
                  <Skeleton className="h-4 w-48" />
                </div>
              )
            ) : (
              <EnrollScan
                enrollData={enrollData}
                codeId={codeId}
                code={code}
                onCodeChange={(v) => {
                  setCode(v)
                  setConfirmError(null)
                }}
                error={confirmError}
              />
            )}

            <AlertDialogFooter>
              <Button variant="ghost" size="sm" onClick={onClose} disabled={isConfirming}>
                Cancel
              </Button>
              <Button
                variant="primary"
                size="sm"
                disabled={!enrollData || isConfirming || code.trim().length === 0}
                onClick={() => void onConfirm()}
              >
                {isConfirming && <Loader2 className="size-3.5 animate-spin" />}
                Verify &amp; enable
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function EnrollScan({
  enrollData,
  codeId,
  code,
  onCodeChange,
  error,
}: {
  enrollData: TwoFactorEnrollResponse
  codeId: string
  code: string
  onCodeChange: (value: string) => void
  error: string | null
}) {
  return (
    <div className="flex flex-col items-center gap-4">
      <QrCode value={enrollData.otpauth_uri} />

      <div className="w-full text-center">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Setup key</p>
        <p className="mt-1 select-all break-all font-mono text-[13px] text-foreground">{enrollData.secret}</p>
      </div>

      <div className="w-full">
        <Label htmlFor={codeId}>6-digit code</Label>
        <Input
          id={codeId}
          className="mt-1.5"
          inputMode="numeric"
          autoComplete="one-time-code"
          autoFocus
          placeholder="123456"
          value={code}
          aria-invalid={!!error}
          onChange={(e) => onCodeChange(e.target.value)}
        />
        {error && (
          <p role="alert" className="mt-1.5 text-xs text-danger">
            {error}
          </p>
        )}
      </div>
    </div>
  )
}

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const [acknowledged, setAcknowledged] = useState(false)
  const [copied, setCopied] = useState(false)
  const ackId = useId()

  const asText = codes.join('\n')

  function onCopy() {
    void navigator.clipboard?.writeText(asText).then(
      () => setCopied(true),
      () => setCopied(false),
    )
  }

  function onDownload() {
    const blob = new Blob([`${asText}\n`], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'inroad-recovery-codes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>Save your recovery codes</AlertDialogTitle>
        <AlertDialogDescription>
          Each code works once if you lose access to your authenticator app. Store them somewhere safe — they
          won't be shown again.
        </AlertDialogDescription>
      </AlertDialogHeader>

      <ul className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-md border border-border bg-surface-2 p-4 font-mono text-[13px] tabular-nums text-foreground">
        {codes.map((c) => (
          <li key={c} className="select-all">
            {c}
          </li>
        ))}
      </ul>

      <div className="flex gap-2">
        <Button variant="outline" size="sm" onClick={onCopy}>
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
        <Button variant="outline" size="sm" onClick={onDownload}>
          <Download className="size-3.5" />
          Download
        </Button>
      </div>

      <label htmlFor={ackId} className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground">
        <input
          id={ackId}
          type="checkbox"
          className="mt-0.5 size-4 accent-primary"
          checked={acknowledged}
          onChange={(e) => setAcknowledged(e.target.checked)}
        />
        <span>I've saved these recovery codes somewhere safe.</span>
      </label>

      <AlertDialogFooter>
        <Button variant="primary" size="sm" disabled={!acknowledged} onClick={onDone}>
          Done
        </Button>
      </AlertDialogFooter>
    </>
  )
}

function DisableDialog({ onClose, onDisabled }: { onClose: () => void; onDisabled: () => void }) {
  const codeId = useId()
  const [disable, { isLoading }] = useAuthTwoFactorDisableMutation()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)

  async function onConfirm() {
    setError(null)
    const result = await disable({ twoFactorCodeRequest: { code: code.trim() } })
    if ('error' in result) {
      const status = httpStatus(result.error)
      setError(
        status === 400 || status === 401
          ? 'That code was incorrect. Enter a current code or a recovery code.'
          : 'Something went wrong. Please try again.',
      )
      return
    }
    onDisabled()
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Turn off two-factor authentication?</AlertDialogTitle>
          <AlertDialogDescription>
            Enter a current 6-digit code (or a recovery code) to confirm. Disabling also signs out your other
            sessions.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div>
          <Label htmlFor={codeId}>Verification code</Label>
          <Input
            id={codeId}
            className="mt-1.5"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            placeholder="123456"
            value={code}
            aria-invalid={!!error}
            onChange={(e) => {
              setCode(e.target.value)
              setError(null)
            }}
          />
          {error && (
            <p role="alert" className="mt-1.5 text-xs text-danger">
              {error}
            </p>
          )}
        </div>

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            size="sm"
            disabled={isLoading || code.trim().length === 0}
            onClick={() => void onConfirm()}
          >
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Disable 2FA
          </Button>
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

function StatusError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex items-center gap-3 border-b border-border px-5 py-4">
      <AlertCircle className="size-5 shrink-0 text-danger" aria-hidden="true" />
      <p className="flex-1 text-[13px] text-muted-foreground">Couldn't load your two-factor status.</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        Retry
      </Button>
    </div>
  )
}

function LoadingRow() {
  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-4">
      <Skeleton className="size-5 rounded-md" />
      <div className="flex-1 space-y-2">
        <Skeleton className="h-3.5 w-40" />
        <Skeleton className="h-2.5 w-64" />
      </div>
      <Skeleton className="h-8 w-24" />
    </div>
  )
}
