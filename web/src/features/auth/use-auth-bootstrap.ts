import { useEffect } from 'react'
import type { AppDispatch } from '@/store'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { setSession, clearSession } from '@/store/slices/auth'
import { api } from '@/store/api'

let bootstrapPromise: Promise<void> | null = null

/**
 * Runs the silent-refresh bootstrap exactly once for the lifetime of the tab:
 * POST /auth/refresh (the httpOnly refresh cookie carries the session) and
 * resolve the in-memory session from the result, falling back to `anon` if
 * there is no valid refresh cookie. Module-level singleton so every call
 * site — the hook below and the `/app` route guard — shares one network
 * request instead of racing the refresh-token rotation.
 */
export function runAuthBootstrap(dispatch: AppDispatch): Promise<void> {
  bootstrapPromise ??= dispatch(api.endpoints.authRefresh.initiate())
    .unwrap()
    .then((session) => {
      dispatch(setSession(session))
    })
    .catch(() => {
      dispatch(clearSession())
    })
  return bootstrapPromise
}

/**
 * Mount-time hook that kicks the bootstrap off as early as possible (intended
 * for `__root.tsx`, so it starts before route guards even evaluate). A no-op
 * once `status` has left `idle`.
 */
export function useAuthBootstrap() {
  const status = useAppSelector((state) => state.auth.status)
  const dispatch = useAppDispatch()

  useEffect(() => {
    if (status === 'idle') {
      void runAuthBootstrap(dispatch)
    }
  }, [status, dispatch])
}
