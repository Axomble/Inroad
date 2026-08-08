import { useEffect, useRef } from 'react'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { Loader2 } from 'lucide-react'
import { AuthLayout } from './auth-layout'
import { useAppDispatch } from '@/store/hooks'
import { setSession } from '@/store/slices/auth'
import { safeReturnTo } from './return-to'
import { useAuthRefreshMutation } from './api'

const routeApi = getRouteApi('/auth/google/callback')

/**
 * Where a SUCCESSFUL Google sign-in lands the browser. This route is a transfer
 * station, not a destination — it holds the user for one request and then sends
 * them on, so it renders a bare progress state rather than a screen worth reading.
 *
 * Its single job is turning the cookie the callback already set into an access
 * token: `POST /auth/refresh`, exactly as the silent-refresh bootstrap does. No
 * token ever travels in the URL. Keeping that on one route means no other route
 * has to know Google sign-in exists.
 *
 * Failures never reach here — the backend redirects them to `/?google_error=…`,
 * where the login screen reports them. Arriving without `signin=ok` therefore means
 * something went wrong upstream, so it's forwarded to that same surface rather than
 * left as a blank page.
 */
export function GoogleCallbackPage() {
  const { signin, return_to: returnTo } = routeApi.useSearch()
  const navigate = useNavigate()
  const dispatch = useAppDispatch()
  const [refresh] = useAuthRefreshMutation()
  // React may run an effect twice (StrictMode); the refresh-token rotation makes a
  // second exchange a real bug, not just a wasted request.
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true

    if (signin !== 'ok') {
      void navigate({ to: '/', search: { google_error: 'server_error' }, replace: true })
      return
    }

    void (async () => {
      const result = await refresh()
      if ('data' in result && result.data) {
        dispatch(setSession(result.data))
        // The server validated `return_to` on the way out, but it arrives here as a
        // URL param, so it is validated again before the browser acts on it — and
        // via a full navigation rather than the SPA router, because the resume
        // target may be the API's /oauth2/authorize rather than an SPA route.
        const resume = safeReturnTo(returnTo)
        if (resume) {
          window.location.assign(resume)
          return
        }
        void navigate({ to: '/app', replace: true })
        return
      }
      // Google said yes but the cookie exchange didn't land — not one of the
      // backend's reason codes, so it borrows the closest one.
      void navigate({ to: '/', search: { google_error: 'exchange_failed' }, replace: true })
    })()
    // Runs once, on mount: the search params are fixed for this navigation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <AuthLayout>
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <Loader2 className="size-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <p role="status" className="text-sm text-muted-foreground">
          Finishing sign-in…
        </p>
      </div>
    </AuthLayout>
  )
}
