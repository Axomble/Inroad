import { useEffect, useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, XCircle } from 'lucide-react'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
import { useAppDispatch } from '@/store/hooks'
import { setSession } from '@/store/slices/auth'
import { AuthLayout } from './auth-layout'
import { useAuthAcceptInviteMutation } from './api'

const routeApi = getRouteApi('/accept-invite')

// Password is optional: the backend only uses it to set up a brand-new
// account (an existing email is just added as a member and keeps its
// current password) — see AcceptInviteRequest / A3 in the phase-2 design.
const schema = z.object({
  password: z.string().min(8, 'Use at least 8 characters').optional().or(z.literal('')),
})
type FormValues = z.infer<typeof schema>

/**
 * Consumes the `?token=` from a workspace-invite link: joins the inviting
 * workspace (linking an existing account, or creating one if the password
 * field is filled in) and signs the caller straight into the app.
 */
export function AcceptInvitePage() {
  const { token } = routeApi.useSearch()
  const {
    register,
    handleSubmit,
    setFocus,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [acceptInvite, { isLoading, error }] = useAuthAcceptInviteMutation()
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const passwordId = useId()

  // 422 means the backend recognized the invite but needs a password to
  // create the account (a brand-new invitee submitted without one) — that's
  // recoverable, so send focus back to the password field instead of
  // treating it like a dead invite (see errorMessage below).
  const errorStatus = (error as { status?: number } | undefined)?.status
  useEffect(() => {
    if (errorStatus === 422) setFocus('password')
  }, [errorStatus, setFocus])

  if (!token) {
    return (
      <AuthLayout>
        <div className="flex flex-col items-center gap-4 text-center">
          <XCircle className="size-8 text-danger" />
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">Invalid or expired invite</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              Ask whoever invited you to send a new invite link.
            </p>
          </div>
          <Button asChild variant="secondary" size="lg" className="mt-2 w-full">
            <Link to="/">Back to sign in</Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  // Re-bind as its own `const`: TS narrows `token` to `string` at this point
  // (the guard above already returned otherwise), but that narrowing doesn't
  // carry into a function declared below it, since it could in principle be
  // invoked later against a differently-typed closure.
  const inviteToken: string = token
  async function onSubmit(values: FormValues) {
    const result = await acceptInvite({
      acceptInviteRequest: { token: inviteToken, password: values.password || undefined },
    })
    if ('data' in result && result.data) {
      dispatch(setSession(result.data))
      navigate({ to: '/app/mailboxes' })
    }
  }

  function errorMessage() {
    if (errorStatus === 422) return 'This is a new account — set a password above to finish joining.'
    return 'This invite is invalid or has expired. Ask for a new one.'
  }

  return (
    <AuthLayout>
      <div className="auth-rise mb-7">
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Workspace invite</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Join the workspace</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          If you don't have an Inroad account yet, set a password below to create one.
        </p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={passwordId}>Password</Label>
          <PasswordInput
            id={passwordId}
            autoComplete="new-password"
            placeholder="Only needed for a new account"
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
          <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {errorMessage()}
          </p>
        )}

        <Button type="submit" variant="primary" size="lg" className="mt-1 w-full" disabled={isLoading}>
          {isLoading && <Loader2 className="animate-spin" />}
          {isLoading ? 'Joining…' : 'Accept invite'}
        </Button>
      </form>
    </AuthLayout>
  )
}
