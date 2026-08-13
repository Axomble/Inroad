import { useId, useState } from 'react'
import { AlertCircle, Check, Copy, KeyRound, Loader2, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, type StatusTone } from '@/components/shared/status-pill'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { ScrollDialogContent, ScrollDialogBody } from '@/components/shared/scroll-dialog'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import type { ApiKey } from '@/store/api'
import { useHasRole } from '@/hooks/use-has-role'
import { formatDateTime, relativeTime } from './session-format'
import { API_KEY_SCOPE_GROUPS } from './api-key-scopes'
import { useAuthApiKeyListQuery, useAuthApiKeyCreateMutation, useAuthApiKeyRevokeMutation } from './api'

type Notice = { tone: 'ok' | 'error'; text: string }

/**
 * Settings → API keys (P6 auth hardening). Admin-gated: minting a scoped API key
 * is a workspace-owner/admin capability on the backend (session + admin-role,
 * never scope-gated), so non-admins get a clear "admins only" state instead of a
 * dead form. This is a UX guard mirroring the server's `RequireRole(admin)` — the
 * server remains the security boundary. Server state lives in RTK Query; creating
 * / revoking invalidates the `ApiKeys` list tag so the view refetches itself.
 */
export function ApiKeysPanel() {
  const isAdmin = useHasRole('admin')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [creating, setCreating] = useState(false)

  const { data, isLoading, isError, refetch } = useAuthApiKeyListQuery(undefined, { skip: !isAdmin })

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Workspace" title="API keys" />
        <EmptyBlock
          title="Admins only"
          description="Ask a workspace owner or admin to create and manage API keys for programmatic access."
        />
      </Page>
    )
  }

  const keys = data?.api_keys ?? []

  return (
    <Page>
      <PageTopbar
        eyebrow="Workspace"
        title="API keys"
        subtitle="Programmatic, scoped access to this workspace"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            Create API key
          </Button>
        }
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <ListError onRetry={() => void refetch()} />
        ) : keys.length === 0 ? (
          <EmptyBlock
            title="No API keys yet"
            description="Create a scoped key to integrate this workspace with scripts, CI, or third-party tools."
          />
        ) : (
          <>
            <SectionBar label="Keys" count={keys.length} />
            {keys.map((key) => (
              <ApiKeyRow key={key.id} apiKey={key} onNotice={setNotice} />
            ))}
          </>
        )}
      </PageBody>

      {creating && (
        <CreateApiKeyDialog
          onClose={() => setCreating(false)}
          onCreated={(name) => setNotice({ tone: 'ok', text: `API key “${name}” was created.` })}
        />
      )}
    </Page>
  )
}

/**
 * Turn a `type="date"` value (`YYYY-MM-DD`, no zone) into an ISO timestamp at
 * the END of that day in the user's LOCAL zone. `new Date('2026-08-01')` parses
 * as UTC midnight, so "expires 2026-08-01" could kill a key a day early for
 * anyone at a negative UTC offset; treating it as 23:59:59.999 local keeps the
 * key valid through the whole chosen day everywhere. Returns `null` only for an
 * unparseable value (unreachable via a native date input, guarded for safety).
 */
function endOfDayLocalIso(date: string): string | null {
  const [year, month, day] = date.split('-').map(Number)
  if (year === undefined || month === undefined || day === undefined) return null
  const local = new Date(year, month - 1, day, 23, 59, 59, 999)
  return Number.isNaN(local.getTime()) ? null : local.toISOString()
}

/** The lifecycle state of a key, derived from its metadata (revoked > expired > active). */
function keyState(apiKey: ApiKey): { tone: StatusTone; label: string } {
  if (apiKey.revoked_at) return { tone: 'failing', label: 'Revoked' }
  if (apiKey.expires_at && new Date(apiKey.expires_at).getTime() <= Date.now()) {
    return { tone: 'paused', label: 'Expired' }
  }
  return { tone: 'running', label: 'Active' }
}

function ApiKeyRow({ apiKey, onNotice }: { apiKey: ApiKey; onNotice: (n: Notice) => void }) {
  const [confirming, setConfirming] = useState(false)
  const [revoke, { isLoading }] = useAuthApiKeyRevokeMutation()
  const state = keyState(apiKey)
  const revocable = !apiKey.revoked_at

  async function onRevoke() {
    const result = await revoke({ id: apiKey.id })
    // Close first so an error banner isn't hidden under the dialog.
    setConfirming(false)
    if ('error' in result) {
      const status = httpStatus(result.error)
      onNotice({
        tone: 'error',
        text:
          status === 404
            ? 'That key was already removed.'
            : "Couldn't revoke that key. Please try again.",
      })
    } else {
      onNotice({ tone: 'ok', text: `API key “${apiKey.name}” was revoked.` })
    }
  }

  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-3.5">
      <KeyRound className="size-5 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{apiKey.name}</span>
          <StatusPill tone={state.tone}>{state.label}</StatusPill>
          <code className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
            {apiKey.prefix}…
          </code>
        </div>
        <div className="mt-1 flex flex-wrap gap-1">
          {apiKey.scopes.map((scope) => (
            <span
              key={scope}
              className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] text-muted-foreground"
            >
              {scope}
            </span>
          ))}
        </div>
        <div className="mt-1 font-mono text-[11px] text-faint">
          created {formatDateTime(apiKey.created_at)}
          {apiKey.last_used_at ? ` · last used ${relativeTime(apiKey.last_used_at)}` : ' · never used'}
          {apiKey.expires_at ? ` · expires ${relativeTime(apiKey.expires_at)}` : ' · no expiry'}
        </div>
      </div>

      {revocable ? (
        <Button
          variant="outline"
          size="sm"
          disabled={isLoading}
          aria-label={`Revoke API key ${apiKey.name}`}
          onClick={() => setConfirming(true)}
        >
          {isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
          Revoke
        </Button>
      ) : (
        <span className="font-mono text-[11px] uppercase tracking-[0.1em] text-faint">Revoked</span>
      )}

      <AlertDialog open={confirming} onOpenChange={(next) => !next && setConfirming(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke this API key?</AlertDialogTitle>
            <AlertDialogDescription>
              Any integration using “{apiKey.name}” will immediately stop working. This can't be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={isLoading}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onRevoke()}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              Revoke key
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

/**
 * Create form → one-time token reveal. The full token is returned by the server
 * EXACTLY ONCE; it lives only in this component's local state (never Redux /
 * persist) and is shown behind an explicit "you won't see this again" warning
 * with copy-to-clipboard. Closing the dialog drops it for good.
 */
function CreateApiKeyDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string) => void }) {
  const nameId = useId()
  const expiryId = useId()
  const rateId = useId()
  const ipId = useId()
  const [create, { isLoading }] = useAuthApiKeyCreateMutation()

  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>([])
  const [expiry, setExpiry] = useState('')
  const [rateLimit, setRateLimit] = useState('')
  const [ipAllowlist, setIpAllowlist] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [token, setToken] = useState<string | null>(null)

  const canSubmit = name.trim().length > 0 && scopes.length > 0 && !isLoading

  function toggleScope(value: string) {
    setError(null)
    setScopes((prev) => (prev.includes(value) ? prev.filter((s) => s !== value) : [...prev, value]))
  }

  async function onSubmit() {
    if (!canSubmit) return
    setError(null)

    const ips = ipAllowlist
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0)
    const rate = rateLimit.trim() === '' ? null : Number(rateLimit)
    if (rate !== null && (!Number.isFinite(rate) || rate <= 0)) {
      setError('Rate limit must be a positive number, or left blank for unlimited.')
      return
    }

    const result = await create({
      apiKeyCreateRequest: {
        name: name.trim(),
        scopes,
        ip_allowlist: ips.length > 0 ? ips : null,
        rate_limit_per_min: rate,
        expires_at: expiry ? endOfDayLocalIso(expiry) : null,
      },
    })

    if ('data' in result && result.data) {
      setToken(result.data.token)
      onCreated(result.data.api_key.name)
      return
    }
    setError(
      httpStatus(result.error) === 400
        ? 'Check the key settings — a scope, expiry, CIDR, or rate limit was rejected.'
        : "Couldn't create the API key. Please try again.",
    )
  }

  const revealed = token !== null

  return (
    <AlertDialog
      open
      onOpenChange={(next) => {
        // The token step must be dismissed explicitly (it's shown only once).
        if (!next && !revealed && !isLoading) onClose()
      }}
    >
      {/* Wide enough for the two-column scope grid; header and actions stay
          pinned while the form body scrolls. */}
      <ScrollDialogContent className="sm:max-w-2xl">
        {revealed ? (
          <TokenReveal token={token} onDone={onClose} />
        ) : (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Create an API key</AlertDialogTitle>
              <AlertDialogDescription>
                Grant only the scopes this integration needs. The full token is shown once, right after you
                create it.
              </AlertDialogDescription>
            </AlertDialogHeader>

            <ScrollDialogBody>
              <div>
                <Label htmlFor={nameId}>Name</Label>
                <Input
                  id={nameId}
                  className="mt-1.5"
                  autoFocus
                  placeholder="e.g. CI deploy bot"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                    setError(null)
                  }}
                />
              </div>

              <fieldset>
                <legend className="text-[13px] font-medium text-foreground">Scopes</legend>
                <p className="mt-0.5 text-[12px] text-muted-foreground">Pick at least one capability.</p>
                <div className="mt-2 grid gap-3 sm:grid-cols-2">
                  {API_KEY_SCOPE_GROUPS.map((group) => (
                    <div key={group.domain} className="rounded-md border border-border p-2.5">
                      <p className="mb-1.5 font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">
                        {group.domain}
                      </p>
                      <div className="flex flex-col gap-1.5">
                        {group.scopes.map((scope) => (
                          <label
                            key={scope.value}
                            className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground"
                          >
                            <input
                              type="checkbox"
                              className="mt-0.5 size-4 accent-primary"
                              checked={scopes.includes(scope.value)}
                              onChange={() => toggleScope(scope.value)}
                            />
                            <span>
                              {scope.label}
                              <span className="block text-[11px] text-muted-foreground">{scope.description}</span>
                            </span>
                          </label>
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              </fieldset>

              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <Label htmlFor={expiryId}>Expires (optional)</Label>
                  <Input
                    id={expiryId}
                    type="date"
                    className="mt-1.5"
                    value={expiry}
                    onChange={(e) => {
                      setExpiry(e.target.value)
                      setError(null)
                    }}
                  />
                </div>
                <div>
                  <Label htmlFor={rateId}>Rate limit / min (optional)</Label>
                  <Input
                    id={rateId}
                    type="number"
                    min={1}
                    inputMode="numeric"
                    className="mt-1.5"
                    placeholder="Unlimited"
                    value={rateLimit}
                    onChange={(e) => {
                      setRateLimit(e.target.value)
                      setError(null)
                    }}
                  />
                </div>
              </div>

              <div>
                <Label htmlFor={ipId}>IP allowlist (optional)</Label>
                <Input
                  id={ipId}
                  className="mt-1.5"
                  placeholder="203.0.113.4, 10.0.0.0/8"
                  value={ipAllowlist}
                  onChange={(e) => {
                    setIpAllowlist(e.target.value)
                    setError(null)
                  }}
                />
                <p className="mt-1 text-[11px] text-muted-foreground">
                  Comma-separated IPs or CIDRs. Leave blank for no restriction.
                </p>
              </div>

              {error && (
                <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
                  {error}
                </p>
              )}
            </ScrollDialogBody>

            <AlertDialogFooter>
              <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
                Cancel
              </Button>
              <Button variant="primary" size="sm" disabled={!canSubmit} onClick={() => void onSubmit()}>
                {isLoading && <Loader2 className="size-3.5 animate-spin" />}
                Create key
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </ScrollDialogContent>
    </AlertDialog>
  )
}

function TokenReveal({ token, onDone }: { token: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false)

  function onCopy() {
    void navigator.clipboard?.writeText(token).then(
      () => setCopied(true),
      () => setCopied(false),
    )
  }

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>Copy your API key now</AlertDialogTitle>
        <AlertDialogDescription>
          This is the only time the full token is shown. Store it somewhere safe — you won't be able to see it
          again. If you lose it, revoke the key and create a new one.
        </AlertDialogDescription>
      </AlertDialogHeader>

      <div className="flex items-center gap-2 rounded-md border border-border bg-surface-2 p-3">
        <code className="min-w-0 flex-1 select-all break-all font-mono text-[12.5px] text-foreground">{token}</code>
        <Button variant="outline" size="sm" onClick={onCopy} aria-label="Copy API key">
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <p role="alert" className="flex items-center gap-2 text-xs text-warn">
        <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
        Treat this token like a password. It grants the scopes you selected.
      </p>

      <AlertDialogFooter>
        <Button variant="primary" size="sm" onClick={onDone}>
          Done
        </Button>
      </AlertDialogFooter>
    </>
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
      title="Couldn't load your API keys"
      description="Something went wrong fetching this workspace's API keys. Please try again."
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
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-64" />
          </div>
          <Skeleton className="h-8 w-20" />
        </div>
      ))}
    </div>
  )
}
