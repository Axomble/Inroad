import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, XCircle } from 'lucide-react'
import { getRouteApi, Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
import { AuthLayout } from './auth-layout'
import { useAuthResetPasswordMutation } from './api'

const routeApi = getRouteApi('/reset-password')

const schema = z.object({
  password: z.string().min(8, 'Use at least 8 characters'),
})
type FormValues = z.infer<typeof schema>

/**
 * Consumes the `?token=` from a reset-password link, sets a new password,
 * and revokes every existing session for the account (see the reset flow's
 * A7 invariant) — the user signs back in fresh afterwards.
 */
export function ResetPasswordPage() {
  const { token } = routeApi.useSearch()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [resetPassword, { isLoading, isSuccess, error }] = useAuthResetPasswordMutation()
  const passwordId = useId()

  async function onSubmit(values: FormValues) {
    if (!token) return
    await resetPassword({ resetPasswordRequest: { token, new_password: values.password } })
  }

  if (!token) {
    return (
      <AuthLayout>
        <div className="flex flex-col items-center gap-4 text-center">
          <XCircle className="size-8 text-danger" />
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">Invalid or expired link</h1>
            <p className="mt-1 text-sm text-muted-foreground">Request a new password reset link and try again.</p>
          </div>
          <Button asChild variant="secondary" size="lg" className="mt-2 w-full">
            <Link to="/forgot-password">Request a new link</Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  if (isSuccess) {
    return (
      <AuthLayout>
        <div className="flex flex-col items-center gap-4 text-center">
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">Password updated</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              You've been signed out everywhere for safety. Sign in with your new password.
            </p>
          </div>
          <Button asChild variant="primary" size="lg" className="mt-2 w-full">
            <Link to="/">Continue to sign in</Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className="auth-rise mb-7">
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Password reset</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Choose a new password</h1>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={passwordId}>New password</Label>
          <PasswordInput
            id={passwordId}
            autoComplete="new-password"
            placeholder="At least 8 characters"
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
            This reset link is invalid or expired. Request a new one and try again.
          </p>
        )}

        <Button type="submit" variant="primary" size="lg" className="mt-1 w-full" disabled={isLoading}>
          {isLoading && <Loader2 className="animate-spin" />}
          {isLoading ? 'Updating…' : 'Update password'}
        </Button>
      </form>
    </AuthLayout>
  )
}
