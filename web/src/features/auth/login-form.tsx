import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/ui/password-input'
import { useAppDispatch } from '@/store/hooks'
import { setSession, setUserIdentity } from '@/store/slices/auth'
import { AuthLayout } from './auth-layout'
import { useAuthLoginMutation } from './api'

const schema = z.object({
  email: z.email('Enter a valid email address'),
  password: z.string().min(1, 'Enter your password'),
})
type FormValues = z.infer<typeof schema>

export function LoginForm() {
  const emailId = useId()
  const passwordId = useId()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [login, { isLoading, error }] = useAuthLoginMutation()
  const dispatch = useAppDispatch()
  const navigate = useNavigate()

  async function onSubmit(values: FormValues) {
    const result = await login({ loginRequest: values })
    if ('data' in result && result.data) {
      dispatch(setSession(result.data))
      // The current session response doesn't carry the user's email; capture
      // it from the form so the avatar/menu can show real initials.
      dispatch(setUserIdentity({ email: values.email }))
      navigate({ to: '/app/mailboxes' })
    }
  }

  return (
    <AuthLayout>
      <div className="auth-rise mb-7" style={{ animationDelay: '120ms' }}>
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Welcome back</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Sign in to your workspace</h1>
      </div>

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
            <a href="#" className="text-xs text-muted-foreground transition-colors hover:text-accent-ink">
              Forgot password?
            </a>
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
            We couldn't sign you in. Check your email and password, then try again.
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
    </AuthLayout>
  )
}
