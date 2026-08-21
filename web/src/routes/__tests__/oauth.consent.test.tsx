import { describe, expect, test, vi } from 'vitest'
import { Route } from '../oauth.consent'
import { makeTestStore } from '@/test/render-with-providers'

// runAuthBootstrap normally issues an /auth/refresh — mocked here to a no-op so
// the guard synchronously falls through to the accessToken check when the slice
// is in the `idle` state (mirrors app.test.tsx).
vi.mock('@/features/auth/use-auth-bootstrap', () => ({
  runAuthBootstrap: vi.fn(async () => {}),
  useAuthBootstrap: () => {},
}))

const CONSENT_URL = 'https://app.inroad.test/oauth/consent?consent_id=c-1'

// The router passes the full arg surface to beforeLoad; our guard only reads
// `context.store` and `location.href`, so a partial cast is fine here.
function runBeforeLoad(store: ReturnType<typeof makeTestStore>) {
  return Route.options.beforeLoad?.({
    context: { store },
    location: { href: CONSENT_URL },
  } as unknown as Parameters<NonNullable<typeof Route.options.beforeLoad>>[0])
}

describe('/oauth/consent beforeLoad', () => {
  test('an unauthenticated store is redirected to login with a return_to back to this consent URL', async () => {
    const store = makeTestStore({ auth: { status: 'anon', accessToken: null } })

    let redirected: unknown = null
    try {
      // A `throw redirect(...)` inside beforeLoad surfaces here as the thrown
      // redirect object; catch it and inspect its target + carried search.
      await runBeforeLoad(store)
    } catch (err) {
      redirected = err
    }

    expect(redirected).toBeDefined()
    const options = (redirected as { options?: { to?: string; search?: { return_to?: string } } } | null)?.options
    expect(options?.to).toBe('/')
    // The user resumes on this exact consent screen after signing in.
    expect(options?.search?.return_to).toBe(CONSENT_URL)
  })

  test('an authenticated store passes through without redirecting', async () => {
    const store = makeTestStore({ auth: { status: 'authed', accessToken: 'tok-abc' } })

    // A pass-through returns (does not throw) — assert no redirect escapes.
    await expect(runBeforeLoad(store)).resolves.toBeUndefined()
  })
})
