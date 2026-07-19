import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useLoginMutation } from './api'

const schema = z.object({
  email: z.string().email('Enter a valid email address'),
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
  const [login, { isLoading, data, error }] = useLoginMutation()

  return (
    <main className="grid min-h-dvh place-items-center bg-background px-4">
      {/* ambient glow, matches the console chrome */}
      <div
        aria-hidden="true"
        className="pointer-events-none fixed inset-0 [background:radial-gradient(900px_460px_at_78%_-10%,rgba(124,92,255,0.16),transparent_60%),radial-gradient(680px_380px_at_8%_110%,rgba(245,165,36,0.07),transparent_55%)]"
      />

      <div className="relative w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-3 text-center">
          <div className="grid size-10 place-items-center rounded-lg bg-primary text-lg font-bold text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.25),0_2px_0_var(--primary-edge),0_8px_20px_rgba(124,92,255,0.35)]">
            I
          </div>
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">Sign in to Inroad</h1>
            <p className="mt-1 text-sm text-muted-foreground">Welcome back — sign in to your workspace.</p>
          </div>
        </div>

        <form
          onSubmit={handleSubmit((v) => login({ loginRequest: v }))}
          noValidate
          className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6 shadow-[0_12px_30px_rgba(0,0,0,0.28)]"
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={emailId}>Email</Label>
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

          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <Label htmlFor={passwordId}>Password</Label>
              <a href="#" className="text-xs text-primary hover:underline">
                Forgot password?
              </a>
            </div>
            <Input
              id={passwordId}
              type="password"
              autoComplete="current-password"
              placeholder="••••••••"
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
              We couldn't sign you in. Check your email and password, then try again.
            </p>
          )}

          <Button type="submit" variant="primary" className="mt-1 w-full" disabled={isLoading}>
            {isLoading && <Loader2 className="animate-spin" />}
            {isLoading ? 'Signing in…' : 'Log in'}
          </Button>

          {data && (
            <p className="text-center text-xs text-muted-foreground">
              Signed in as <span className="font-mono text-foreground">{data.user_id}</span>
            </p>
          )}
        </form>

        <p className="mt-5 text-center text-sm text-muted-foreground">
          New to Inroad?{' '}
          <a href="#" className="font-medium text-primary hover:underline">
            Create an account
          </a>
        </p>
      </div>
    </main>
  )
}
