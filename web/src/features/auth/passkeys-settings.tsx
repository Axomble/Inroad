import { useId, useState } from 'react'
import { AlertCircle, Fingerprint, KeyRound, Loader2, ShieldCheck, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { PageTopbar, SectionBar, EmptyBlock } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import type { PasskeyInfo } from '@/store/api'
import { formatDateTime, relativeTime } from './session-format'
import {
  isWebAuthnAvailable,
  runRegistrationCeremony,
  webauthnErrorMessage,
} from './webauthn'
import {
  useAuthPasskeyListQuery,
  useAuthPasskeyDeleteMutation,
  useAuthPasskeyRegisterBeginMutation,
  useAuthPasskeyRegisterFinishMutation,
} from './api'

type Notice = { tone: 'ok' | 'error'; text: string }

/**
 * Security → Passkeys (P4 auth hardening). Lists the caller's registered
 * passkeys and drives the WebAuthn REGISTRATION ceremony (begin → browser
 * `navigator.credentials.create` → finish). Server state lives entirely in RTK
 * Query: registering / deleting invalidates the `Passkeys` list tag so this
 * panel refetches itself.
 *
 * The whole section is feature-detected: on a browser with no
 * `PublicKeyCredential`, or where the ceremony can't run, we render nothing
 * rather than a dead button.
 */
export function PasskeysSettings() {
  if (!isWebAuthnAvailable()) return null
  return <PasskeysPanel />
}

function PasskeysPanel() {
  const { data, isLoading, isError, refetch } = useAuthPasskeyListQuery()
  const [notice, setNotice] = useState<Notice | null>(null)
  const [adding, setAdding] = useState(false)

  const passkeys = data?.passkeys ?? []

  return (
    <div className="flex flex-col">
      <PageTopbar
        eyebrow="Security"
        title="Passkeys"
        subtitle="Sign in with your device instead of a password"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setAdding(true)}>
            <Fingerprint className="size-4" />
            Add a passkey
          </Button>
        }
      />

      {notice && <NoticeBanner notice={notice} />}

      {isLoading ? (
        <LoadingRow />
      ) : isError ? (
        <StatusError onRetry={() => void refetch()} />
      ) : passkeys.length === 0 ? (
        <EmptyBlock
          title="No passkeys yet"
          description="Add a passkey to sign in with your fingerprint, face, or device PIN — no password required."
        />
      ) : (
        <>
          <SectionBar label="Registered" count={passkeys.length} />
          {passkeys.map((passkey) => (
            <PasskeyRow key={passkey.id} passkey={passkey} onNotice={setNotice} />
          ))}
        </>
      )}

      {adding && (
        <AddPasskeyDialog
          onClose={() => setAdding(false)}
          onAdded={(label) => {
            setAdding(false)
            setNotice({ tone: 'ok', text: `Passkey “${label}” is ready to use.` })
          }}
        />
      )}
    </div>
  )
}

function PasskeyRow({ passkey, onNotice }: { passkey: PasskeyInfo; onNotice: (n: Notice) => void }) {
  const [confirming, setConfirming] = useState(false)
  const [remove, { isLoading }] = useAuthPasskeyDeleteMutation()

  async function onDelete() {
    const result = await remove({ id: passkey.id })
    // Close the dialog first, so an error banner isn't hidden underneath it.
    setConfirming(false)
    if ('error' in result) {
      const status = httpStatus(result.error)
      onNotice({
        tone: 'error',
        text:
          status === 404
            ? 'That passkey was already removed.'
            : "Couldn't remove that passkey. Please try again.",
      })
    } else {
      onNotice({ tone: 'ok', text: `Passkey “${passkey.label}” was removed.` })
    }
  }

  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-3.5">
      <KeyRound className="size-5 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />

      <div className="min-w-0 flex-1">
        <span className="truncate text-[13.5px] font-medium text-foreground">{passkey.label}</span>
        <div className="mt-0.5 font-mono text-[11px] text-faint">
          added {formatDateTime(passkey.created_at)}
          {passkey.last_used_at ? ` · last used ${relativeTime(passkey.last_used_at)}` : ' · never used'}
        </div>
      </div>

      <Button
        variant="outline"
        size="sm"
        disabled={isLoading}
        aria-label={`Remove passkey ${passkey.label}`}
        onClick={() => setConfirming(true)}
      >
        {isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
        Remove
      </Button>

      <AlertDialog open={confirming} onOpenChange={(next) => !next && setConfirming(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove this passkey?</AlertDialogTitle>
            <AlertDialogDescription>
              You won't be able to sign in with “{passkey.label}” anymore. You can add it again later.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={isLoading}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onDelete()}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              Remove passkey
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

/**
 * Names the passkey, then runs the registration ceremony: begin (server issues
 * WebAuthn options + a ceremony session id) → `navigator.credentials.create`
 * via {@link runRegistrationCeremony} → finish. Every failure — a 501 (passkeys
 * not configured), a cancelled prompt, or a rejected attestation — resolves the
 * spinner and surfaces an inline message, never a stuck state.
 */
function AddPasskeyDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (label: string) => void }) {
  const labelId = useId()
  const [begin] = useAuthPasskeyRegisterBeginMutation()
  const [finish] = useAuthPasskeyRegisterFinishMutation()
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onCreate() {
    const trimmed = label.trim()
    if (trimmed.length === 0) return
    setError(null)
    setBusy(true)
    try {
      const begun = await begin()
      if ('error' in begun) {
        setError(
          httpStatus(begun.error) === 501
            ? 'Passkeys are not configured on this server. Contact your administrator.'
            : "Couldn't start passkey setup. Please try again.",
        )
        return
      }

      const credential = await runRegistrationCeremony(begun.data.publicKey)

      const done = await finish({
        passkeyFinishRequest: { session_id: begun.data.session_id, credential, label: trimmed },
      })
      if ('error' in done) {
        const status = httpStatus(done.error)
        setError(
          status === 409
            ? 'This device already has a passkey registered.'
            : status === 501
              ? 'Passkeys are not configured on this server.'
              : "That didn't work. Please try adding the passkey again.",
        )
        return
      }
      onAdded(trimmed)
    } catch (err) {
      // A cancelled / unsupported / insecure-origin ceremony throws a DOMException.
      setError(webauthnErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Add a passkey</AlertDialogTitle>
          <AlertDialogDescription>
            Give this passkey a name you'll recognise, then follow your browser's prompt to create it with your
            fingerprint, face, or device PIN.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div>
          <Label htmlFor={labelId}>Passkey name</Label>
          <Input
            id={labelId}
            className="mt-1.5"
            autoFocus
            placeholder="e.g. MacBook Touch ID"
            value={label}
            aria-invalid={!!error}
            disabled={busy}
            onChange={(e) => {
              setLabel(e.target.value)
              setError(null)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && label.trim().length > 0 && !busy) void onCreate()
            }}
          />
          {error && (
            <p role="alert" className="mt-1.5 text-xs text-danger">
              {error}
            </p>
          )}
        </div>

        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            disabled={busy || label.trim().length === 0}
            onClick={() => void onCreate()}
          >
            {busy && <Loader2 className="size-3.5 animate-spin" />}
            {busy ? 'Waiting for your device…' : 'Create passkey'}
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
      <p className="flex-1 text-[13px] text-muted-foreground">Couldn't load your passkeys.</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        Retry
      </Button>
    </div>
  )
}

function LoadingRow() {
  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-3.5">
      <Skeleton className="size-5 rounded-md" />
      <div className="flex-1 space-y-2">
        <Skeleton className="h-3.5 w-40" />
        <Skeleton className="h-2.5 w-56" />
      </div>
      <Skeleton className="h-8 w-24" />
    </div>
  )
}
