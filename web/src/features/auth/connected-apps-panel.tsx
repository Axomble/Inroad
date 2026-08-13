import { useId, useState } from 'react'
import { AlertCircle, Check, Copy, Loader2, Plug, Plus, ShieldCheck, Trash2, X } from 'lucide-react'
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
import { ScrollDialogContent, ScrollDialogBody } from '@/components/shared/scroll-dialog'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { useHasRole } from '@/hooks/use-has-role'
import { httpStatus } from '@/lib/rtk-error'
import type { OAuth2Client } from '@/store/api'
import { formatDateTime } from './session-format'
import { OAUTH_SCOPE_GROUPS } from './oauth-scopes'
import {
  useOauth2ListClientsQuery,
  useOauth2RegisterMutation,
  useOauth2RevokeClientMutation,
} from './oauth-provider-api'

type Notice = { tone: 'ok' | 'error'; text: string }

/**
 * Settings → Connected apps (auth F4). Admin-gated: registering an OAuth client is a
 * workspace-admin capability on the backend (session + admin role), so non-admins get
 * a clear "admins only" state instead of a dead form. This is a UX guard mirroring the
 * server's RequireRole(admin); the server stays the security boundary. Server state
 * lives in RTK Query; registering / revoking invalidates the `OAuthClients` list tag so
 * the view refetches itself.
 */
export function ConnectedAppsPanel() {
  const isAdmin = useHasRole('admin')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [registering, setRegistering] = useState(false)

  const { data, isLoading, isError, refetch } = useOauth2ListClientsQuery(undefined, { skip: !isAdmin })

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Workspace" title="Connected apps" />
        <EmptyBlock
          title="Admins only"
          description="Ask a workspace owner or admin to register and manage OAuth apps for this workspace."
        />
      </Page>
    )
  }

  const clients = data?.clients ?? []

  return (
    <Page>
      <PageTopbar
        eyebrow="Workspace"
        title="Connected apps"
        subtitle="OAuth 2.1 apps authorized to access this workspace"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setRegistering(true)}>
            <Plus className="size-4" />
            Register an app
          </Button>
        }
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        <McpAccessCard />

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <ListError onRetry={() => void refetch()} />
        ) : clients.length === 0 ? (
          <EmptyBlock
            title="No connected apps yet"
            description="Register an OAuth app to let a third-party integration request scoped access to this workspace on a user's behalf."
          />
        ) : (
          <>
            <SectionBar label="Apps" count={clients.length} />
            {clients.map((client) => (
              <ClientRow key={client.client_id} client={client} onNotice={setNotice} />
            ))}
          </>
        )}
      </PageBody>

      {registering && (
        <RegisterAppDialog
          onClose={() => setRegistering(false)}
          onCreated={(name) => setNotice({ tone: 'ok', text: `“${name}” was registered.` })}
        />
      )}
    </Page>
  )
}

function McpAccessCard() {
  const [copied, setCopied] = useState<'endpoint' | 'metadata' | null>(null)

  const values = {
    endpoint: '/v1/mcp',
    metadata: '/.well-known/oauth-protected-resource',
  } as const

  async function copy(kind: keyof typeof values) {
    try {
      await navigator.clipboard.writeText(`${window.location.origin}${values[kind]}`)
      setCopied(kind)
      window.setTimeout(() => setCopied((current) => (current === kind ? null : current)), 1800)
    } catch {
      setCopied(null)
    }
  }

  return (
    <section className="mb-6 rounded-lg border border-border bg-surface-1 p-5" aria-labelledby="mcp-access-title">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 id="mcp-access-title" className="text-sm font-semibold text-foreground">
            MCP server access
          </h2>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Connect an MCP-compatible agent to Inroad with an OAuth app and the scopes it needs.
          </p>
        </div>
        <StatusPill tone="running">Available</StatusPill>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        {(Object.keys(values) as Array<keyof typeof values>).map((kind) => {
          const label = kind === 'endpoint' ? 'Server endpoint' : 'Protected-resource metadata'
          return (
            <div key={kind} className="min-w-0 rounded-md border border-border bg-surface-2 p-3">
              <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-faint">{label}</div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">{values[kind]}</code>
                <Button
                  variant="ghost"
                  size="sm"
                  className="shrink-0"
                  aria-label={`Copy ${label.toLowerCase()}`}
                  onClick={() => void copy(kind)}
                >
                  {copied === kind ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                  {copied === kind ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </div>
          )
        })}
      </div>
    </section>
  )
}

function ClientRow({ client, onNotice }: { client: OAuth2Client; onNotice: (n: Notice) => void }) {
  const [confirming, setConfirming] = useState(false)
  const [revoke, { isLoading }] = useOauth2RevokeClientMutation()
  const revoked = !!client.revoked_at
  const scopes = client.scope.split(/\s+/).filter(Boolean)

  async function onRevoke() {
    const result = await revoke({ clientId: client.client_id })
    // Close first so an error banner isn't hidden under the dialog.
    setConfirming(false)
    if ('error' in result) {
      const status = httpStatus(result.error)
      onNotice({
        tone: 'error',
        text: status === 404 ? 'That app was already removed.' : "Couldn't revoke that app. Please try again.",
      })
    } else {
      onNotice({ tone: 'ok', text: `“${client.client_name}” was revoked.` })
    }
  }

  return (
    <div className="flex items-center gap-4 border-b border-border px-5 py-3.5">
      <Plug className="size-5 shrink-0 text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />

      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-medium text-foreground">{client.client_name}</span>
          <StatusPill tone={revoked ? 'failing' : 'running'}>{revoked ? 'Revoked' : 'Active'}</StatusPill>
          <StatusPill tone="draft" dot={false}>
            {client.client_type}
          </StatusPill>
          <code className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
            {client.client_id}
          </code>
        </div>
        {scopes.length > 0 && (
          <div className="mt-1 flex flex-wrap gap-1">
            {scopes.map((scope) => (
              <span
                key={scope}
                className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] text-muted-foreground"
              >
                {scope}
              </span>
            ))}
          </div>
        )}
        <div className="mt-1 truncate font-mono text-[11px] text-faint">
          {client.redirect_uris.join(' · ')} · registered {formatDateTime(client.created_at)}
        </div>
      </div>

      {revoked ? (
        <span className="font-mono text-[11px] uppercase tracking-[0.1em] text-faint">Revoked</span>
      ) : (
        <Button
          variant="outline"
          size="sm"
          disabled={isLoading}
          aria-label={`Revoke app ${client.client_name}`}
          onClick={() => setConfirming(true)}
        >
          {isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
          Revoke
        </Button>
      )}

      <AlertDialog open={confirming} onOpenChange={(next) => !next && setConfirming(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke this app?</AlertDialogTitle>
            <AlertDialogDescription>
              “{client.client_name}” will immediately lose access, and its tokens stop working. This can't be
              undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={isLoading}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onRevoke()}>
              {isLoading && <Loader2 className="size-3.5 animate-spin" />}
              Revoke app
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

/** Loopback hosts an `http` (non-TLS) redirect_uri is allowed on, for native apps. */
function isLoopbackHost(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]'
}

/**
 * Client-side redirect_uri check mirroring the backend rules, for fast feedback:
 * https anywhere, or http only on a loopback host; no fragment; and (implied by the
 * scheme check) no `javascript:` / `data:`. The backend re-validates — this only
 * spares a round-trip on an obvious mistake. Returns an error string, or `null` if ok.
 */
function redirectUriError(raw: string): string | null {
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return 'Enter a valid absolute URL.'
  }
  if (url.hash) return 'Remove the "#" fragment from the URL.'
  if (url.protocol === 'https:') return null
  if (url.protocol === 'http:' && isLoopbackHost(url.hostname)) return null
  return 'Use https, or http on localhost.'
}

/** A fresh redirect-URL row with a stable client-only id for React keys. */
function newUriRow(): { id: string; value: string } {
  return { id: crypto.randomUUID(), value: '' }
}

/**
 * Register form → one-time secret reveal. A confidential client's `client_secret` is
 * returned by the server EXACTLY ONCE; it's held only in this component's local state
 * for display, and is never PERSISTED (it transits the in-memory RTK Query cache from
 * the register mutation, but the API cache is memory-only, so it is neither
 * written to browser storage nor logged. It's shown behind a "you won't see this
 * again" warning with copy-to-clipboard, and closing the dialog drops it for good. A
 * public (PKCE) client has no secret — only its `client_id` is shown.
 */
function RegisterAppDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string) => void }) {
  const nameId = useId()
  const [register, { isLoading }] = useOauth2RegisterMutation()

  const [name, setName] = useState('')
  // Keyed rows so React reconciles them stably as rows are added/removed (no
  // array-index key), even though the list itself is never reordered.
  const [redirectUris, setRedirectUris] = useState<{ id: string; value: string }[]>(() => [newUriRow()])
  const [scopes, setScopes] = useState<string[]>([])
  const [confidential, setConfidential] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [registered, setRegistered] = useState<OAuth2Client | null>(null)

  const filledUris = redirectUris.map((u) => u.value.trim()).filter((u) => u.length > 0)
  const canSubmit = name.trim().length > 0 && filledUris.length > 0 && scopes.length > 0 && !isLoading

  function clearError() {
    setError(null)
  }

  function setUri(id: string, value: string) {
    clearError()
    setRedirectUris((prev) => prev.map((u) => (u.id === id ? { ...u, value } : u)))
  }

  function addUri() {
    setRedirectUris((prev) => [...prev, newUriRow()])
  }

  function removeUri(id: string) {
    clearError()
    setRedirectUris((prev) => (prev.length === 1 ? prev : prev.filter((u) => u.id !== id)))
  }

  function toggleScope(value: string) {
    clearError()
    setScopes((prev) => (prev.includes(value) ? prev.filter((s) => s !== value) : [...prev, value]))
  }

  async function onSubmit() {
    if (!canSubmit) return
    setError(null)

    const badUri = filledUris.map((u) => redirectUriError(u)).find((e): e is string => e !== null)
    if (badUri) {
      setError(badUri)
      return
    }

    const result = await register({
      oAuth2RegisterRequest: {
        client_name: name.trim(),
        redirect_uris: filledUris,
        scope: scopes.join(' '),
        token_endpoint_auth_method: confidential ? 'client_secret_basic' : 'none',
      },
    })

    if ('data' in result && result.data) {
      setRegistered(result.data)
      onCreated(result.data.client_name)
      return
    }
    setError(
      httpStatus(result.error) === 400
        ? 'Check the app settings — a redirect URL, scope, or client type was rejected.'
        : "Couldn't register the app. Please try again.",
    )
  }

  const revealed = registered !== null

  return (
    <AlertDialog
      open
      onOpenChange={(next) => {
        // The credential step must be dismissed explicitly (secret shown once).
        if (!next && !revealed && !isLoading) onClose()
      }}
    >
      {/* Wide enough for the two-column scope grid; header and actions stay
          pinned while the form body scrolls. */}
      <ScrollDialogContent className="sm:max-w-2xl">
        {registered ? (
          <CredentialReveal client={registered} onDone={onClose} />
        ) : (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Register an app</AlertDialogTitle>
              <AlertDialogDescription>
                Grant only the scopes this app needs. A confidential app's secret is shown once, right after you
                register it.
              </AlertDialogDescription>
            </AlertDialogHeader>

            <ScrollDialogBody>
              <div>
                <Label htmlFor={nameId}>App name</Label>
                <Input
                  id={nameId}
                  className="mt-1.5"
                  autoFocus
                  placeholder="e.g. Acme CRM sync"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                    clearError()
                  }}
                />
              </div>

              <fieldset>
                <legend className="text-[13px] font-medium text-foreground">Redirect URLs</legend>
                <p className="mt-0.5 text-[12px] text-muted-foreground">
                  Where users return after authorizing. https, or http on localhost. Exact-matched at sign-in.
                </p>
                <div className="mt-2 flex flex-col gap-2">
                  {redirectUris.map((uri, index) => (
                    <div key={uri.id} className="flex items-center gap-2">
                      <Input
                        aria-label={`Redirect URL ${index + 1}`}
                        placeholder="https://app.example.com/oauth/callback"
                        value={uri.value}
                        onChange={(e) => setUri(uri.id, e.target.value)}
                      />
                      {redirectUris.length > 1 && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove redirect URL ${index + 1}`}
                          onClick={() => removeUri(uri.id)}
                        >
                          <X className="size-4" />
                        </Button>
                      )}
                    </div>
                  ))}
                </div>
                <Button type="button" variant="ghost" size="sm" className="mt-2" onClick={addUri}>
                  <Plus className="size-3.5" />
                  Add another URL
                </Button>
              </fieldset>

              <fieldset>
                <legend className="text-[13px] font-medium text-foreground">Scopes</legend>
                <p className="mt-0.5 text-[12px] text-muted-foreground">Pick at least one capability.</p>
                <div className="mt-2 grid gap-3 sm:grid-cols-2">
                  {OAUTH_SCOPE_GROUPS.map((group) => (
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

              <fieldset>
                <legend className="text-[13px] font-medium text-foreground">Client type</legend>
                <div className="mt-2 flex flex-col gap-2">
                  <label className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground">
                    <input
                      type="radio"
                      name="client-type"
                      className="mt-0.5 size-4 accent-primary"
                      checked={!confidential}
                      onChange={() => {
                        setConfidential(false)
                        clearError()
                      }}
                    />
                    <span>
                      Public (PKCE)
                      <span className="block text-[11px] text-muted-foreground">
                        For SPAs and native apps that can't keep a secret. Recommended.
                      </span>
                    </span>
                  </label>
                  <label className="flex cursor-pointer items-start gap-2 text-[13px] text-foreground">
                    <input
                      type="radio"
                      name="client-type"
                      className="mt-0.5 size-4 accent-primary"
                      checked={confidential}
                      onChange={() => {
                        setConfidential(true)
                        clearError()
                      }}
                    />
                    <span>
                      Confidential
                      <span className="block text-[11px] text-muted-foreground">
                        For server-side apps. Issues a client secret, shown once.
                      </span>
                    </span>
                  </label>
                </div>
              </fieldset>

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
                Register app
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </ScrollDialogContent>
    </AlertDialog>
  )
}

function CredentialReveal({ client, onDone }: { client: OAuth2Client; onDone: () => void }) {
  const hasSecret = typeof client.client_secret === 'string' && client.client_secret.length > 0

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>{hasSecret ? 'Copy your client secret now' : 'App registered'}</AlertDialogTitle>
        <AlertDialogDescription>
          {hasSecret
            ? 'This is the only time the client secret is shown. Store it somewhere safe — you won\'t be able to see it again. If you lose it, revoke this app and register a new one.'
            : "This is a public (PKCE) client, so there's no secret. Use the client ID below in your app's OAuth configuration."}
        </AlertDialogDescription>
      </AlertDialogHeader>

      <div className="flex flex-col gap-3">
        <CopyField label="Client ID" value={client.client_id} />
        {hasSecret && <CopyField label="Client secret" value={client.client_secret as string} secret />}
      </div>

      {hasSecret && (
        <p role="alert" className="flex items-center gap-2 text-xs text-warn">
          <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
          Treat this secret like a password.
        </p>
      )}

      <AlertDialogFooter>
        <Button variant="primary" size="sm" onClick={onDone}>
          Done
        </Button>
      </AlertDialogFooter>
    </>
  )
}

function CopyField({ label, value, secret = false }: { label: string; value: string; secret?: boolean }) {
  const [copied, setCopied] = useState(false)

  function onCopy() {
    void navigator.clipboard?.writeText(value).then(
      () => setCopied(true),
      () => setCopied(false),
    )
  }

  return (
    <div>
      <p className="mb-1 font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">{label}</p>
      <div className="flex items-center gap-2 rounded-md border border-border bg-surface-2 p-3">
        <code className="min-w-0 flex-1 select-all break-all font-mono text-[12.5px] text-foreground">{value}</code>
        <Button variant="outline" size="sm" onClick={onCopy} aria-label={`Copy ${secret ? 'client secret' : 'client ID'}`}>
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
    </div>
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
      title="Couldn't load your connected apps"
      description="Something went wrong fetching this workspace's OAuth apps. Please try again."
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
