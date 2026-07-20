import { useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useAppSelector } from '@/store/hooks'

/**
 * Watches the in-memory access token. When it transitions from a truthy value
 * to `null` — a background reauth failure, the RTK Query base query's fallback
 * `clearSession()`, or an explicit logout — we kick the user back to the login
 * page immediately, so they don't sit staring at a shell that will only 401.
 *
 * The transition (rather than "if null, redirect") matters: on first mount
 * during bootstrap the token is null but the app is legitimately deciding
 * whether to /app/* or /. The route guard in `routes/app.tsx` handles that
 * initial case.
 */
export function useAuthGuard() {
  const accessToken = useAppSelector((s) => s.auth.accessToken)
  const navigate = useNavigate()
  const prevToken = useRef<string | null>(accessToken)

  useEffect(() => {
    if (prevToken.current && !accessToken) {
      void navigate({ to: '/', replace: true })
    }
    prevToken.current = accessToken
  }, [accessToken, navigate])
}
