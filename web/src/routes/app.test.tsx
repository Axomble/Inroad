import { describe, expect, test, vi } from 'vitest'
import { Route } from './app'
import { makeTestStore } from '@/test/render-with-providers'

// runAuthBootstrap normally issues an /auth/refresh — mocked here to a no-op
// so the guard synchronously falls through to the accessToken check when the
// slice is in the `idle` state.
vi.mock('@/features/auth/use-auth-bootstrap', () => ({
  runAuthBootstrap: vi.fn(async () => {}),
  useAuthBootstrap: () => {},
}))

describe('/app beforeLoad', () => {
  test('redirects to / when the injected store has no session', async () => {
    const store = makeTestStore({ auth: { status: 'anon', accessToken: null } })

    let redirected: unknown = null
    try {
      // The route's `beforeLoad` throws a `redirect()` object when it wants
      // to redirect — we catch it and inspect the target.
      await Route.options.beforeLoad?.({
        context: { store },
        // The rest of the beforeLoad arg surface isn't touched by our guard
        // implementation; a partial cast is fine for this unit-level assertion.
      } as unknown as Parameters<NonNullable<typeof Route.options.beforeLoad>>[0])
    } catch (err) {
      redirected = err
    }

    expect(redirected).toBeDefined()
    // TanStack Router's `redirect()` returns a Response with `.options` on it,
    // and throws that Response when `opts.throw` is set (which the router does
    // for you inside beforeLoad — a `throw redirect(...)` puts the caller here).
    const options = (redirected as { options?: { to?: string } } | null)?.options
    expect(options?.to).toBe('/')
  })
})
