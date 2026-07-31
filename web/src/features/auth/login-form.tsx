import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ArrowLeft, Fingerprint, Loader2, Mail } from 'lucide-react'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
import { useAppDispatch } from '@/store/hooks'
import { setSession, setUserIdentity, type SessionResponse } from '@/store/slices/auth'
import { httpStatus, isFetchBaseQueryError, retryAfterSeconds } from '@/lib/rtk-error'
import { AuthLayout } from './auth-layout'
import {
  isWebAuthnAvailable,
  runAuthenticationCeremony,
  webauthnErrorMessage,
} from './webauthn'
import {
  useAuthLoginMutation,
  useAuthTwoFactorVerifyMutation,
  useAuthPasskeyLoginBeginMutation,
  useAuthPasskeyLoginFinishMutation,
  useAuthEmailOtpStartMutation,
  useAuthEmailOtpVerifyMutation,
} from './api'

const routeApi = getRouteApi('/')

const schema = z.object({
  email: z.email('Enter a valid email address'),
  password: z.string().min(1, 'Enter your password'),
})
type FormValues = z.infer<typeof schema>

/**
 * A 2FA challenge in flight, held in component state only (never localStorage):
 * the challenge token is single-use and short-lived, and is exchanged for a
 * session at /auth/2fa/verify. `email` rides along so the completed session can
 * show real initials in the avatar (the session body doesn't carry it).
 */
type Pending = { challenge: string; email: string }

/**
 * A `return_to` is only honoured when it resolves to a same-origin target. We
 * resolve it against `window.location.origin` and compare origins, then return
 * the NORMALIZED same-origin path (`pathname + search + hash`) — never the raw
 * string. This blocks the login form from being turned into an open redirect:
 * a prefix check isn't enough (`/\evil.com` and `/\/evil.com` pass a `//` guard
 * but WHATWG-normalize backslashes to `//`, so a browser would navigate to
 * `https://evil.com/`). Resolving-then-verifying rejects protocol-relative
 * (`//evil.com`), backslash (`/\evil.com`), absolute (`https://evil.com`), and
 * scheme (`javascript:`, `data:`) inputs — they resolve off-origin or throw —
 * while allowing a legitimate same-origin path including the API's
 * `/oauth2/authorize?...` resume. Returning `pathname + search + hash` strips any
 * authority so the validated path can't smuggle an off-origin target. Callers
 * then navigate via `window.location.assign`, because the target may be the API's
 * `/oauth2/authorize` (not an SPA route) as well as an SPA path.
 */
function safeReturnTo(raw: string | undefined): string | null {
  if (!raw) return null
  try {
    const u = new URL(raw, window.location.origin)
    if (u.origin !== window.location.origin) return null
    return u.pathname + u.search + u.hash
  } catch {
    return null
  }
}

export function LoginForm() {
  const { return_to: returnTo } = routeApi.useSearch()
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const [pending, setPending] = useState<Pending | null>(null)
  // Which first-factor path the user is on. A discoverable passkey login skips
  // the 2FA gate server-side, so it has no view of its own — it's an action on
  // the credentials step that lands straight on a session.
  const [view, setView] = useState<'credentials' | 'email-otp'>('credentials')
  // A message carried back to the credentials step when a challenge is abandoned
  // or dies (expired / used up), so the user understands why they're here again.
  const [returnNotice, setReturnNotice] = useState<string | null>(null)

  // `email` is optional: a discoverable passkey login resolves the user
  // server-side and returns no email, so there's nothing to seed the avatar with.
  function completeLogin(session: SessionResponse, email?: string) {
    dispatch(setSession(session))
    if (email) dispatch(setUserIdentity({ email }))
    const resume = safeReturnTo(returnTo)
    if (resume) {
      // Full navigation, not the SPA router: the resume target may be the API's
      // /oauth2/authorize (a top-level browser nav the backend handles), not just
      // an SPA route. The refresh cookie login just set survives the reload, so the
      // bootstrap restores the in-memory session on the resumed page.
      window.location.assign(resume)
      return
    }
    navigate({ to: '/app' })
  }

  function onChallenge(challenge: string, email: string) {
    setReturnNotice(null)
    setPending({ challenge, email })
  }

  if (pending) {
    return (
      <AuthLayout>
        <TwoFactorChallenge
          pending={pending}
          onVerified={(session) => completeLogin(session, pending.email)}
          onExpired={(message) => {
            setReturnNotice(message)
            setPending(null)
          }}
        />
      </AuthLayout>
    )
  }

  if (view === 'email-otp') {
    return (
      <AuthLayout>
        <EmailOtpStep
          onSession={completeLogin}
          onChallenge={onChallenge}
          onBack={() => setView('credentials')}
        />
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <CredentialsStep
        returnNotice={returnNotice}
        onSession={completeLogin}
        onChallenge={onChallenge}
        onUseEmailCode={() => {
          setReturnNotice(null)
          setView('email-otp')
        }}
      />
    </AuthLayout>
  )
}

function CredentialsStep({
  returnNotice,
  onSession,
  onChallenge,
  onUseEmailCode,
}: {
  returnNotice: string | null
  onSession: (session: SessionResponse, email?: string) => void
  onChallenge: (challenge: string, email: string) => void
  onUseEmailCode: () => void
}) {
  const emailId = useId()
  const passwordId = useId()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [login, { isLoading, error }] = useAuthLoginMutation()

  async function onSubmit(values: FormValues) {
    const result = await login({ loginRequest: values })
    if ('data' in result && result.data) {
      const data = result.data
      // The login response is a union: a session, OR a 2FA challenge (no tokens)
      // for accounts with a confirmed second factor.
      if ('two_factor_required' in data) {
        onChallenge(data.challenge, values.email)
        return
      }
      onSession(data, values.email)
    }
  }

  return (
    <>
      <div className="auth-rise mb-7" style={{ animationDelay: '120ms' }}>
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Welcome back</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Sign in to your workspace</h1>
      </div>

      {returnNotice && (
        <p
          role="status"
          className="auth-rise mb-4 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn"
        >
          {returnNotice}
        </p>
      )}

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="flex flex-col gap-4">
        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '180ms' }}>
          <Label htmlFor={emailId}>Email</Label>
          <Input
            id={emailId}
            type="email"
            autoComplete="email"
            autoFocus
            placeholder="you@company.com"
            aria-invalid={!!errors.email}
            {...register('email')}
          />
          {errors.email && (
            <span role="alert" className="text-xs text-danger">
              {errors.email.message}
            </span>
          )}
        </div>

        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '240ms' }}>
          <div className="flex items-center justify-between">
            <Label htmlFor={passwordId}>Password</Label>
            <Link
              to="/forgot-password"
              className="text-xs text-muted-foreground transition-colors hover:text-accent-ink"
            >
              Forgot password?
            </Link>
          </div>
          <PasswordInput
            id={passwordId}
            autoComplete="current-password"
            placeholder="Enter your password"
            aria-invalid={!!errors.password}
            {...register('password')}
          />
          {errors.password && (
            <span role="alert" className="text-xs text-danger">
              {errors.password.message}
            </span>
          )}
        </div>

        {error && (
          <p
            role="alert"
            className="auth-rise rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
          >
            {httpStatus(error) === 429
              ? 'Too many sign-in attempts. Please wait a moment, then try again.'
              : "We couldn't sign you in. Check your email and password, then try again."}
          </p>
        )}

        <Button
          type="submit"
          variant="primary"
          size="lg"
          className="auth-rise mt-1 w-full"
          style={{ animationDelay: '300ms' }}
          disabled={isLoading}
        >
          {isLoading && <Loader2 className="animate-spin" />}
          {isLoading ? 'Signing in…' : 'Log in'}
        </Button>
      </form>

      <div className="auth-rise mt-5" style={{ animationDelay: '320ms' }}>
        <div className="flex items-center gap-3">
          <span className="h-px flex-1 bg-border" />
          <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-faint">or</span>
          <span className="h-px flex-1 bg-border" />
        </div>

        <div className="mt-4 flex flex-col gap-2.5">
          <PasskeyLoginButton onSession={onSession} />
          <Button type="button" variant="outline" size="lg" className="w-full" onClick={onUseEmailCode}>
            <Mail className="size-4" aria-hidden="true" />
            Sign in with an email code
          </Button>
        </div>
      </div>

      <p className="auth-rise mt-6 text-center text-sm text-muted-foreground" style={{ animationDelay: '360ms' }}>
        New to Inroad?{' '}
        <Link to="/register" className="font-medium text-accent-ink hover:underline">
          Create an account
        </Link>
      </p>
    </>
  )
}

/**
 * Discoverable (usernameless) passkey login. Feature-detected: renders nothing
 * where the browser has no WebAuthn support, and self-hides if the server
 * answers 501 (passkeys not configured, `INROAD_RP_ID` unset) — so it's never a
 * dead button. Success mints a session exactly like a password login; the server
 * skips the 2FA gate because a user-verified passkey is already strong auth, so
 * there's no challenge branch here.
 */
function PasskeyLoginButton({ onSession }: { onSession: (session: SessionResponse, email?: string) => void }) {
  const [begin] = useAuthPasskeyLoginBeginMutation()
  const [finish] = useAuthPasskeyLoginFinishMutation()
  const [supported, setSupported] = useState(isWebAuthnAvailable())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!supported) return null

  async function onClick() {
    setError(null)
    setBusy(true)
    try {
      const begun = await begin()
      if ('error' in begun) {
        // Passkeys not configured on this server — retire the button quietly.
        if (httpStatus(begun.error) === 501) {
          setSupported(false)
          return
        }
        setError('Passkey sign-in is unavailable right now. Please try another method.')
        return
      }

      const credential = await runAuthenticationCeremony(begun.data.publicKey)

      const done = await finish({
        passkeyFinishRequest: { session_id: begun.data.session_id, credential },
      })
      if ('data' in done && done.data) {
        onSession(done.data)
        return
      }
      if (httpStatus(done.error) === 501) {
        setSupported(false)
        return
      }
      setError("That passkey didn't work. Please try again, or use another method.")
    } catch (err) {
      // A cancelled / unsupported / insecure-origin ceremony throws a DOMException.
      setError(webauthnErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <Button
        type="button"
        variant="outline"
        size="lg"
        className="w-full"
        disabled={busy}
        onClick={() => void onClick()}
      >
        {busy ? <Loader2 className="size-4 animate-spin" /> : <Fingerprint className="size-4" aria-hidden="true" />}
        {busy ? 'Waiting for your device…' : 'Sign in with a passkey'}
      </Button>
      {error && (
        <p role="alert" className="text-xs text-danger">
          {error}
        </p>
      )}
    </div>
  )
}

/**
 * Passwordless email-code login. Step 1 requests a code (always the same generic
 * acknowledgement — no account-enumeration oracle); step 2 exchanges the code for
 * a session, OR routes through the SAME 2FA challenge as a password login when
 * the account has a confirmed second factor. 429s surface a clear rate-limit
 * message (honouring Retry-After when present).
 */
function EmailOtpStep({
  onSession,
  onChallenge,
  onBack,
}: {
  onSession: (session: SessionResponse, email?: string) => void
  onChallenge: (challenge: string, email: string) => void
  onBack: () => void
}) {
  const emailId = useId()
  const codeId = useId()
  const [start, { isLoading: isStarting }] = useAuthEmailOtpStartMutation()
  const [verify, { isLoading: isVerifying }] = useAuthEmailOtpVerifyMutation()

  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [sent, setSent] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onStart(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    const parsed = z.email().safeParse(email.trim())
    if (!parsed.success) {
      setError('Enter a valid email address.')
      return
    }
    const result = await start({ emailOtpStartRequest: { email: parsed.data } })
    if ('error' in result) {
      setError(rateLimitMessage(result.error) ?? "Couldn't send a code right now. Please try again.")
      return
    }
    // Anti-enumeration: the same acknowledgement regardless of whether the
    // address maps to an account.
    setSent(true)
  }

  async function onVerify(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    const result = await verify({ emailOtpVerifyRequest: { email: email.trim(), code: code.trim() } })
    if ('data' in result && result.data) {
      const data = result.data
      if ('two_factor_required' in data) {
        onChallenge(data.challenge, email.trim())
        return
      }
      onSession(data, email.trim())
      return
    }
    const rate = rateLimitMessage(result.error)
    setError(rate ?? "That code didn't work. Check it and try again, or request a new one.")
  }

  if (!sent) {
    return (
      <form onSubmit={(e) => void onStart(e)} noValidate className="flex flex-col gap-4">
        <div className="auth-rise mb-1" style={{ animationDelay: '80ms' }}>
          <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Email code</p>
          <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Sign in with an email code</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            Enter your email and we'll send you a single-use sign-in code.
          </p>
        </div>

        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '140ms' }}>
          <Label htmlFor={emailId}>Email</Label>
          <Input
            id={emailId}
            type="email"
            autoComplete="email"
            autoFocus
            placeholder="you@company.com"
            value={email}
            aria-invalid={!!error}
            onChange={(e) => {
              setEmail(e.target.value)
              setError(null)
            }}
          />
          {error && (
            <span role="alert" className="text-xs text-danger">
              {error}
            </span>
          )}
        </div>

        <Button
          type="submit"
          variant="primary"
          size="lg"
          className="auth-rise mt-1 w-full"
          style={{ animationDelay: '200ms' }}
          disabled={isStarting || email.trim().length === 0}
        >
          {isStarting && <Loader2 className="animate-spin" />}
          {isStarting ? 'Sending…' : 'Send code'}
        </Button>

        <button
          type="button"
          className="auth-rise flex items-center justify-center gap-1 text-xs text-muted-foreground transition-colors hover:text-accent-ink"
          style={{ animationDelay: '240ms' }}
          onClick={onBack}
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          Back to password sign-in
        </button>
      </form>
    )
  }

  return (
    <form onSubmit={(e) => void onVerify(e)} noValidate className="flex flex-col gap-4">
      <div className="auth-rise mb-1" style={{ animationDelay: '80ms' }}>
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Email code</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Check your email</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          If an account exists for <span className="font-medium text-foreground">{email.trim()}</span>, we sent it
          a 6-digit code. Enter it below.
        </p>
      </div>

      <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '140ms' }}>
        <Label htmlFor={codeId}>Sign-in code</Label>
        <Input
          id={codeId}
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
          <span role="alert" className="text-xs text-danger">
            {error}
          </span>
        )}
      </div>

      <Button
        type="submit"
        variant="primary"
        size="lg"
        className="auth-rise mt-1 w-full"
        style={{ animationDelay: '200ms' }}
        disabled={isVerifying || code.trim().length === 0}
      >
        {isVerifying && <Loader2 className="animate-spin" />}
        {isVerifying ? 'Verifying…' : 'Verify'}
      </Button>

      <div className="auth-rise flex items-center justify-between text-xs" style={{ animationDelay: '240ms' }}>
        <button
          type="button"
          className="text-muted-foreground transition-colors hover:text-accent-ink"
          onClick={() => {
            setSent(false)
            setCode('')
            setError(null)
          }}
        >
          Use a different email
        </button>
        <button
          type="button"
          className="flex items-center gap-1 text-muted-foreground transition-colors hover:text-accent-ink"
          onClick={onBack}
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          Back to password
        </button>
      </div>
    </form>
  )
}

/**
 * A rate-limit (429) message, honouring `Retry-After` when the server sends it
 * (as seconds or an HTTP date). Returns `null` for any non-429 error so callers
 * fall through to their own generic message.
 */
function rateLimitMessage(err: unknown): string | null {
  if (httpStatus(err) !== 429) return null
  const seconds = retryAfterSeconds(err)
  if (seconds !== null) {
    const mins = Math.ceil(seconds / 60)
    return seconds <= 90
      ? `Too many attempts. Please try again in about ${seconds} seconds.`
      : `Too many attempts. Please try again in about ${mins} minute${mins === 1 ? '' : 's'}.`
  }
  return 'Too many attempts. Please wait a moment, then try again.'
}

/**
 * The second step of the login gate: exchange the single-use challenge plus a
 * TOTP (or recovery) code for a session. The server answers any failure with a
 * flat 401, so a wrong code and a dead challenge are indistinguishable from
 * here — we surface a combined "incorrect or expired" message and always offer
 * a "Start over" path back to the credentials step. If the backend does tag the
 * error body as expired, we return automatically.
 */
function TwoFactorChallenge({
  pending,
  onVerified,
  onExpired,
}: {
  pending: Pending
  onVerified: (session: SessionResponse) => void
  onExpired: (message: string) => void
}) {
  const codeId = useId()
  const [verify, { isLoading }] = useAuthTwoFactorVerifyMutation()
  const [code, setCode] = useState('')
  const [useRecovery, setUseRecovery] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    const result = await verify({
      twoFactorVerifyRequest: { challenge: pending.challenge, code: code.trim() },
    })
    if ('data' in result && result.data) {
      onVerified(result.data)
      return
    }
    if (isChallengeDead(result.error)) {
      onExpired('That verification request expired. Please sign in again.')
      return
    }
    setError(
      useRecovery
        ? "That recovery code didn't work. Check it and try again."
        : "That code didn't work. Check your authenticator app and try again.",
    )
  }

  return (
    <form onSubmit={(e) => void onSubmit(e)} noValidate className="flex flex-col gap-4">
      <div className="auth-rise mb-1" style={{ animationDelay: '80ms' }}>
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Two-factor authentication</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Enter your code</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {useRecovery
            ? 'Enter one of your saved recovery codes.'
            : 'Enter the 6-digit code from your authenticator app.'}
        </p>
      </div>

      <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '140ms' }}>
        <Label htmlFor={codeId}>{useRecovery ? 'Recovery code' : 'Authentication code'}</Label>
        <Input
          id={codeId}
          inputMode={useRecovery ? 'text' : 'numeric'}
          autoComplete="one-time-code"
          autoFocus
          placeholder={useRecovery ? 'xxxx-xxxx' : '123456'}
          value={code}
          aria-invalid={!!error}
          onChange={(e) => {
            setCode(e.target.value)
            setError(null)
          }}
        />
        {error && (
          <span role="alert" className="text-xs text-danger">
            {error}
          </span>
        )}
      </div>

      <Button
        type="submit"
        variant="primary"
        size="lg"
        className="auth-rise mt-1 w-full"
        style={{ animationDelay: '200ms' }}
        disabled={isLoading || code.trim().length === 0}
      >
        {isLoading && <Loader2 className="animate-spin" />}
        {isLoading ? 'Verifying…' : 'Verify'}
      </Button>

      <div className="auth-rise flex items-center justify-between text-xs" style={{ animationDelay: '240ms' }}>
        <button
          type="button"
          className="text-muted-foreground transition-colors hover:text-accent-ink"
          onClick={() => {
            setUseRecovery((v) => !v)
            setCode('')
            setError(null)
          }}
        >
          {useRecovery ? 'Use an authenticator code' : 'Use a recovery code'}
        </button>
        <button
          type="button"
          className="flex items-center gap-1 text-muted-foreground transition-colors hover:text-accent-ink"
          onClick={() => onExpired('')}
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          Start over
        </button>
      </div>
    </form>
  )
}

/**
 * Whether a verify error signals a dead challenge (expired / used up) rather
 * than a mistyped code. The server returns a flat 401 either way; this only
 * fires if the body carries an explicit tag, which the caller uses to bounce
 * back to the credentials step automatically.
 */
function isChallengeDead(err: unknown): boolean {
  if (!isFetchBaseQueryError(err)) return false
  const code = (err.data as { error?: string } | undefined)?.error
  return code === 'challenge_expired' || code === 'challenge_not_found' || code === 'challenge_exhausted'
}
