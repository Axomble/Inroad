import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
import { useAppDispatch } from '@/store/hooks'
import { setSession, setUserIdentity, type SessionResponse } from '@/store/slices/auth'
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import { AuthLayout } from './auth-layout'
import { useAuthLoginMutation, useAuthTwoFactorVerifyMutation } from './api'

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

export function LoginForm() {
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const [pending, setPending] = useState<Pending | null>(null)
  // A message carried back to the credentials step when a challenge is abandoned
  // or dies (expired / used up), so the user understands why they're here again.
  const [returnNotice, setReturnNotice] = useState<string | null>(null)

  function completeLogin(session: SessionResponse, email: string) {
    dispatch(setSession(session))
    dispatch(setUserIdentity({ email }))
    navigate({ to: '/app/mailboxes' })
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

  return (
    <AuthLayout>
      <CredentialsStep
        returnNotice={returnNotice}
        onSession={completeLogin}
        onChallenge={(challenge, email) => {
          setReturnNotice(null)
          setPending({ challenge, email })
        }}
      />
    </AuthLayout>
  )
}

function CredentialsStep({
  returnNotice,
  onSession,
  onChallenge,
}: {
  returnNotice: string | null
  onSession: (session: SessionResponse, email: string) => void
  onChallenge: (challenge: string, email: string) => void
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

      <p className="auth-rise mt-6 text-center text-sm text-muted-foreground" style={{ animationDelay: '340ms' }}>
        New to Inroad?{' '}
        <Link to="/register" className="font-medium text-accent-ink hover:underline">
          Create an account
        </Link>
      </p>
    </>
  )
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
