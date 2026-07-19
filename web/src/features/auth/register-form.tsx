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
import { cn } from '@/lib/utils'
import { useAppDispatch } from '@/store/hooks'
import { setSession } from '@/store/slices/auth'
import { AuthLayout } from './auth-layout'
import { useRegisterMutation } from './api'

const schema = z.object({
  workspaceName: z.string().min(2, 'Give your workspace a name'),
  email: z.email('Enter a valid email address'),
  password: z.string().min(8, 'Use at least 8 characters'),
})
type FormValues = z.infer<typeof schema>

/** Cheap, dependency-free strength heuristic: length + character variety. */
function strength(pw: string): number {
  if (!pw) return 0
  let score = 0
  if (pw.length >= 8) score++
  if (pw.length >= 12) score++
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) score++
  if (/\d/.test(pw) && /[^A-Za-z0-9]/.test(pw)) score++
  return Math.min(score, 4)
}
const STRENGTH_LABEL = ['', 'Weak', 'Fair', 'Good', 'Strong']
const STRENGTH_COLOR = ['bg-border', 'bg-danger', 'bg-warn', 'bg-primary', 'bg-ok']

export function RegisterForm() {
  const workspaceId = useId()
  const emailId = useId()
  const passwordId = useId()
  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })
  const [registerAccount, { isLoading, error }] = useRegisterMutation()
  const dispatch = useAppDispatch()
  const navigate = useNavigate()

  const pwScore = strength(watch('password') ?? '')

  async function onSubmit(values: FormValues) {
    const result = await registerAccount({
      registerRequest: {
        workspace_name: values.workspaceName,
        email: values.email,
        password: values.password,
      },
    })
    if ('data' in result && result.data) {
      dispatch(
        setSession({
          token: result.data.token,
          userId: result.data.user_id,
          workspaceId: result.data.workspace_id,
        }),
      )
      navigate({ to: '/app/mailboxes' })
    }
  }

  return (
    <AuthLayout>
      <div className="auth-rise mb-7" style={{ animationDelay: '120ms' }}>
        <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-faint">Get started</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-foreground">Create your workspace</h1>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="flex flex-col gap-4">
        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '160ms' }}>
          <Label htmlFor={workspaceId}>Workspace name</Label>
          <Input
            id={workspaceId}
            autoComplete="organization"
            autoFocus
            placeholder="Acme Outbound"
            aria-invalid={!!errors.workspaceName}
            {...register('workspaceName')}
          />
          {errors.workspaceName && (
            <span role="alert" className="text-xs text-danger">
              {errors.workspaceName.message}
            </span>
          )}
        </div>

        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '210ms' }}>
          <Label htmlFor={emailId}>Work email</Label>
          <Input
            id={emailId}
            type="email"
            autoComplete="email"
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

        <div className="auth-rise flex flex-col gap-1.5" style={{ animationDelay: '260ms' }}>
          <Label htmlFor={passwordId}>Password</Label>
          <PasswordInput
            id={passwordId}
            autoComplete="new-password"
            placeholder="At least 8 characters"
            aria-invalid={!!errors.password}
            {...register('password')}
          />
          <div className="mt-1 flex items-center gap-2" aria-hidden={!watch('password')}>
            <div className="flex flex-1 gap-1">
              {[1, 2, 3, 4].map((seg) => (
                <span
                  key={seg}
                  className={cn(
                    'h-1 flex-1 rounded-full transition-colors',
                    seg <= pwScore ? STRENGTH_COLOR[pwScore] : 'bg-border',
                  )}
                />
              ))}
            </div>
            {pwScore > 0 && <span className="w-10 text-right font-mono text-[10px] text-muted-foreground">{STRENGTH_LABEL[pwScore]}</span>}
          </div>
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
            We couldn't create your account. That email may already be registered — try signing in instead.
          </p>
        )}

        <Button
          type="submit"
          variant="primary"
          size="lg"
          className="auth-rise mt-1 w-full"
          style={{ animationDelay: '310ms' }}
          disabled={isLoading}
        >
          {isLoading && <Loader2 className="animate-spin" />}
          {isLoading ? 'Creating workspace…' : 'Create workspace'}
        </Button>

        <p className="auth-rise text-center text-[11px] leading-relaxed text-faint" style={{ animationDelay: '340ms' }}>
          By continuing you agree to the Terms of Service and Privacy Policy.
        </p>
      </form>

      <p className="auth-rise mt-6 text-center text-sm text-muted-foreground" style={{ animationDelay: '360ms' }}>
        Already have an account?{' '}
        <Link to="/" className="font-medium text-primary hover:underline">
          Sign in
        </Link>
      </p>
    </AuthLayout>
  )
}
