import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, MailCheck } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { AuthLayout } from './auth-layout'
import { useAuthForgotPasswordMutation } from './api'

const schema = z.object({
  email: z.email('Enter a valid email address'),
})
type FormValues = z.infer<typeof schema>

/**
 * Requests a password-reset email. The backend always answers 204 whether or
 * not the address is registered (no account-existence leak — see
 * docs/superpowers/specs/2026-07-22-production-auth-phase-2-design.md, A6),
 * so this always shows the same "check your inbox" state once submitted.
 */
export function ForgotPasswordPage() {
  const emailId = useId()
  const [sent, setSent] = useState(false)
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [forgotPassword, { isLoading }] = useAuthForgotPasswordMutation()

  async function onSubmit(values: FormValues) {
    await forgotPassword({ forgotPasswordRequest: values })
    setSent(true)
  }

  if (sent) {
    return (
      <AuthLayout>
        <div className="flex flex-col items-center gap-4 text-center">
          <MailCheck className="size-8 text-primary" />
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">Check your inbox</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              If an account exists for that email, we've sent a link to reset your password.
            </p>
          </div>
          <Button asChild variant="secondary" size="lg" className="mt-2 w-full">
            <Link to="/">Back to sign in</Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className="auth-rise mb-7">
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Password reset</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Forgot your password?</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Enter the email on your account and we'll send you a reset link.
        </p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
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

        <Button type="submit" variant="primary" size="lg" className="mt-1 w-full" disabled={isLoading}>
          {isLoading && <Loader2 className="animate-spin" />}
          {isLoading ? 'Sending…' : 'Send reset link'}
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        Remembered it?{' '}
        <Link to="/" className="font-medium text-primary hover:underline">
          Sign in
        </Link>
      </p>
    </AuthLayout>
  )
}
