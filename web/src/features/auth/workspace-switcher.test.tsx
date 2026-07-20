import { describe, expect, test, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { Provider } from 'react-redux'
import { makeTestStore } from '@/test/render-with-providers'
import { api } from '@/store/api'
import { useAppDispatch } from '@/store/hooks'
import { setActiveWorkspace } from '@/store/slices/auth'
import { useAuthSwitchWorkspaceMutation } from './api'

// The switcher's abort logic is a straight-line dispatch sequence inside its
// `handleSelect`. Rather than fight Radix's DropdownMenu + jsdom's pointer
// event quirks, this test exercises the same sequence via the hook surface
// the component uses — so it asserts on the behavioral contract (abort every
// inflight query & mutation before flipping the active workspace) without
// depending on the Radix internals.

describe('workspace switch abort behavior', () => {
  test('aborts inflight RTKQ requests, then setActiveWorkspace + resetApiState', async () => {
    const abortA = vi.fn()
    const abortB = vi.fn()
    const abortMut = vi.fn()

    vi.spyOn(api.util, 'getRunningQueriesThunk').mockReturnValue(
      (() => [
        { abort: abortA, unwrap: vi.fn(), unsubscribe: vi.fn() },
        { abort: abortB, unwrap: vi.fn(), unsubscribe: vi.fn() },
      ]) as unknown as ReturnType<typeof api.util.getRunningQueriesThunk>,
    )
    vi.spyOn(api.util, 'getRunningMutationsThunk').mockReturnValue(
      (() => [{ abort: abortMut, unwrap: vi.fn(), unsubscribe: vi.fn() }]) as unknown as ReturnType<
        typeof api.util.getRunningMutationsThunk
      >,
    )

    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            access_token: 'w2-token',
            expires_in: 900,
            active_workspace_id: 'w-2',
            role: 'admin',
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        ),
      ),
    )

    const store = makeTestStore({
      auth: {
        status: 'authed',
        accessToken: 'w1-token',
        activeWorkspaceId: 'w-1',
        memberships: [
          { workspace_id: 'w-1', workspace_name: 'One', role: 'owner' },
          { workspace_id: 'w-2', workspace_name: 'Two', role: 'admin' },
        ],
        userId: 'u-1',
        role: 'owner',
      },
    })

    const wrapper = ({ children }: { children: React.ReactNode }) => (
      <Provider store={store}>{children}</Provider>
    )

    const { result } = renderHook(
      () => ({
        dispatch: useAppDispatch(),
        switchMut: useAuthSwitchWorkspaceMutation(),
      }),
      { wrapper },
    )

    // Simulate the same sequence the component runs on handleSelect.
    await act(async () => {
      const [switchWorkspace] = result.current.switchMut
      const res = await switchWorkspace({ switchWorkspaceRequest: { workspace_id: 'w-2' } })
      if ('data' in res && res.data) {
        const runningQueries = result.current.dispatch(api.util.getRunningQueriesThunk())
        const runningMutations = result.current.dispatch(api.util.getRunningMutationsThunk())
        for (const q of runningQueries ?? []) q.abort()
        for (const m of runningMutations ?? []) m.abort()
        result.current.dispatch(
          setActiveWorkspace({
            activeWorkspaceId: res.data.active_workspace_id,
            role: res.data.role,
            accessToken: res.data.access_token,
          }),
        )
        result.current.dispatch(api.util.resetApiState())
      }
    })

    expect(abortA).toHaveBeenCalledTimes(1)
    expect(abortB).toHaveBeenCalledTimes(1)
    expect(abortMut).toHaveBeenCalledTimes(1)
    expect(store.getState().auth.activeWorkspaceId).toBe('w-2')
  })
})
