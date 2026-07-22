import { useEffect, useRef } from 'react'
import { getRouteApi, Link } from '@tanstack/react-router'
import { CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { AuthLayout } from './auth-layout'
import { useAuthVerifyEmailMutation } from './api'

const routeApi = getRouteApi('/verify-email')

/**
 * Consumes the `?token=` from a verify-email link on mount and confirms the
 * account's email address. Public route — reachable while logged out (the
 * token itself is the credential) as well as logged in.
 */
export function VerifyEmailPage() {
  const { token } = routeApi.useSearch()
  const [verifyEmail, { isLoading, isSuccess, isError }] = useAuthVerifyEmailMutation()
  // Guards against the effect firing twice under React StrictMode, which
  // would otherwise burn the (single-use) token on its own re-invocation.
  const firedRef = useRef(false)

  useEffect(() => {
    if (firedRef.current || !token) return
    firedRef.current = true
    void verifyEmail({ verifyEmailRequest: { token } })
  }, [token, verifyEmail])

  const failed = !token || isError

  return (
    <AuthLayout>
      <div className="flex flex-col items-center gap-4 text-center">
        {!failed && (isLoading || !isSuccess) && (
          <>
            <Loader2 className="size-8 animate-spin text-muted-foreground" />
            <div>
              <h1 className="text-xl font-semibold tracking-tight text-foreground">Verifying your email…</h1>
              <p className="mt-1 text-sm text-muted-foreground">Just a moment.</p>
            </div>
          </>
        )}

        {!failed && isSuccess && (
          <>
            <CheckCircle2 className="size-8 text-ok" />
            <div>
              <h1 className="text-xl font-semibold tracking-tight text-foreground">Email verified</h1>
              <p className="mt-1 text-sm text-muted-foreground">You're all set — your address is confirmed.</p>
            </div>
            <Button asChild variant="primary" size="lg" className="mt-2 w-full">
              <Link to="/">Continue to sign in</Link>
            </Button>
          </>
        )}

        {failed && (
          <>
            <XCircle className="size-8 text-danger" />
            <div>
              <h1 className="text-xl font-semibold tracking-tight text-foreground">Invalid or expired link</h1>
              <p className="mt-1 text-sm text-muted-foreground">
                This verification link is invalid or expired. Sign in and resend it from your account.
              </p>
            </div>
            <Button asChild variant="secondary" size="lg" className="mt-2 w-full">
              <Link to="/">Back to sign in</Link>
            </Button>
          </>
        )}
      </div>
    </AuthLayout>
  )
}
