import { useState } from 'react'
import { getRouteApi, Link } from '@tanstack/react-router'
import { AlertCircle, Check, Loader2, ShieldCheck, XCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useAppSelector } from '@/store/hooks'
import { httpStatus } from '@/lib/rtk-error'
import type { OAuth2ConsentData } from '@/store/api'
import { scopeConsentLabel } from './oauth-scopes'
import { useOauth2ConsentDataQuery, useOauth2ConsentDecideMutation } from './oauth-provider-api'

const routeApi = getRouteApi('/oauth/consent')

/**
 * OAuth 2.1 consent screen. The backend's /oauth2/authorize redirects a logged-in
 * resource owner here with an opaque `consent_id`; this screen shows them exactly
 * which third-party app is asking, which scopes it wants, and which workspace the
 * grant covers, then records an explicit approve/deny. There is NO auto-approval and
 * no dark pattern: Deny is a peer of Approve, not a hidden or de-emphasised control.
 *
 * The route guard (routes/oauth.consent.tsx) has already ensured a session exists.
 * On a decision the backend returns an EXTERNAL `redirect_to` (back to the client
 * app); we hand the browser off with a full navigation to exactly that URL — never
 * an SPA route, and never a URL we construct ourselves (anti-open-redirect).
 */
export function OAuthConsentPage() {
  const { consent_id: consentId } = routeApi.useSearch()

  const {
    data,
    isLoading,
    isError,
    error,
  } = useOauth2ConsentDataQuery({ consentId: consentId ?? '' }, { skip: !consentId })

  if (!consentId) {
    return (
      <ConsentShell>
        <ConsentError
          title="Missing authorization request"
          description="This link is missing its authorization details. Return to the app you were using and start again."
        />
      </ConsentShell>
    )
  }

  if (isLoading) {
    return (
      <ConsentShell>
        <div
          role="status"
          aria-live="polite"
          className="flex flex-col items-center gap-3 px-6 py-16 text-center"
        >
          <Loader2 className="size-6 animate-spin text-muted-foreground" aria-hidden="true" />
          <p className="text-sm text-muted-foreground">Loading authorization request…</p>
        </div>
      </ConsentShell>
    )
  }

  if (isError || !data) {
    // 404 = unknown / expired / consumed / not-this-user's. Anything else is an
    // unexpected transport/server error; both leave the user unable to proceed, so
    // both get a clear terminal screen (differentiated copy).
    const expired = httpStatus(error) === 404
    return (
      <ConsentShell>
        <ConsentError
          title={expired ? 'This authorization request is invalid or has expired' : 'Something went wrong'}
          description={
            expired
              ? 'For your security, authorization requests are single-use and expire quickly. Return to the app and start again.'
              : "We couldn't load this authorization request. Return to the app and try again."
          }
        />
      </ConsentShell>
    )
  }

  return (
    <ConsentShell>
      <ConsentCard consentId={consentId} data={data} />
    </ConsentShell>
  )
}

function ConsentCard({
  consentId,
  data,
}: {
  consentId: string
  data: OAuth2ConsentData
}) {
  const memberships = useAppSelector((s) => s.auth.memberships)
  const activeWorkspaceId = useAppSelector((s) => s.auth.activeWorkspaceId)
  const workspaceName = memberships.find((m) => m.workspace_id === activeWorkspaceId)?.workspace_name

  const [decide, { isLoading }] = useOauth2ConsentDecideMutation()
  const [error, setError] = useState<string | null>(null)

  const redirectHost = hostOf(data.redirect_uri)

  async function submit(decision: 'approve' | 'deny') {
    setError(null)
    const result = await decide({ oAuth2ConsentDecision: { consent_id: consentId, decision } })
    if ('data' in result && result.data) {
      // EXTERNAL hand-off back to the third-party app. Full navigation to exactly
      // the backend-validated redirect_to — not the SPA router, not a value we build.
      window.location.assign(result.data.redirect_to)
      return
    }
    setError(
      httpStatus(result.error) === 404
        ? 'This authorization request has expired. Return to the app and start again.'
        : "We couldn't record your decision. Please try again.",
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col items-center gap-3 text-center">
        <div className="grid size-11 place-items-center rounded-xl border border-border bg-surface-2">
          <ShieldCheck className="size-5 text-accent-ink" aria-hidden="true" strokeWidth={1.75} />
        </div>
        <div>
          <h1 className="text-lg font-semibold tracking-tight text-foreground">
            <span className="text-accent-ink">{data.client_name}</span> wants to access your Inroad account
          </h1>
          {workspaceName && (
            <p className="mt-1.5 text-sm text-muted-foreground">
              Access will be granted to the{' '}
              <span className="font-medium text-foreground">{workspaceName}</span> workspace.
            </p>
          )}
        </div>
      </div>

      <div className="rounded-lg border border-border">
        <div className="border-b border-border px-4 py-2.5 font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">
          This app will be able to
        </div>
        <ul className="flex flex-col">
          {data.requested_scopes.map((scope) => (
            <li key={scope} className="flex items-start gap-2.5 border-b border-border px-4 py-2.5 last:border-b-0">
              <Check className="mt-0.5 size-4 shrink-0 text-ok" aria-hidden="true" />
              <span className="text-[13.5px] text-foreground">{scopeConsentLabel(scope)}</span>
            </li>
          ))}
        </ul>
      </div>

      <p className="text-center text-xs text-muted-foreground">
        After you approve, you'll be returned to{' '}
        <span className="font-mono text-foreground">{redirectHost ?? data.redirect_uri}</span>.
      </p>

      {error && (
        <p
          role="alert"
          className="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
        >
          <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />
          {error}
        </p>
      )}

      <div className="flex flex-col-reverse gap-2.5 sm:flex-row">
        <Button
          variant="outline"
          size="lg"
          className="flex-1"
          disabled={isLoading}
          onClick={() => void submit('deny')}
        >
          Deny
        </Button>
        <Button
          variant="primary"
          size="lg"
          className="flex-1"
          disabled={isLoading}
          onClick={() => void submit('approve')}
        >
          {isLoading && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
          Approve
        </Button>
      </div>
    </div>
  )
}

/** Centered, single-purpose shell for the consent decision (no app chrome). */
function ConsentShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid min-h-dvh place-items-center bg-background px-4 py-10">
      <div className="w-full max-w-md">
        <div className="mb-6 flex items-center justify-center gap-2">
          <div className="grid size-8 place-items-center rounded-lg bg-primary text-sm font-bold text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.25),0_2px_0_var(--primary-edge)]">
            I
          </div>
          <span className="text-[15px] font-bold tracking-tight">Inroad</span>
        </div>
        <div className="rounded-xl border border-border bg-card p-6 sm:p-8">{children}</div>
      </div>
    </div>
  )
}

function ConsentError({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex flex-col items-center gap-4 text-center">
      <XCircle className="size-8 text-danger" aria-hidden="true" />
      <div>
        <h1 className="text-lg font-semibold tracking-tight text-foreground">{title}</h1>
        <p className="mt-1.5 text-sm text-muted-foreground">{description}</p>
      </div>
      <Button asChild variant="secondary" size="lg" className="mt-1 w-full">
        <Link to="/">Return to Inroad</Link>
      </Button>
    </div>
  )
}

/** The host of a URL for display, or `null` if it can't be parsed. */
function hostOf(url: string): string | null {
  try {
    return new URL(url).host
  } catch {
    return null
  }
}
