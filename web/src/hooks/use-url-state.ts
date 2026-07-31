import { useCallback } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'

export interface UrlStateOptions {
  /**
   * Push a history entry per write instead of replacing the current one.
   * Defaults to `false` (replace), which is what you want for anything driven by
   * typing — a five-character filter must not put five entries on the history
   * stack, or Back stops meaning "the page I came from". Opt in to `push` for a
   * deliberate, discrete change the user should be able to undo with Back.
   */
  push?: boolean
}

/**
 * `useState`, but the value lives in a URL search param.
 *
 * Use it for any view state that should be shareable, bookmarkable, and
 * survive a reload: a filter, a sort order, an open tab, a selected row. The
 * URL becomes the single source of truth, so there is no local copy to drift
 * out of sync with it.
 *
 * Route-agnostic by design — `useSearch({ strict: false })` and a `to`-less
 * `useNavigate()` both operate on whichever route is currently rendered, so one
 * hook serves every screen. (`getRouteApi('/some/path')` would hardcode one
 * route per copy of this logic.)
 *
 * Writes merge into the existing search object, so unrelated params on the same
 * route survive — e.g. changing a filter on `/app/mailboxes` won't drop the
 * OAuth callback's `connected` param before its banner has been read.
 *
 * Setting the empty string removes the key entirely rather than leaving `?q=`
 * behind, so a cleared control produces a clean URL.
 *
 * **Route requirement:** TanStack's `validateSearch` is authoritative — a param
 * the route's validator doesn't return is stripped on the next navigation. Any
 * route using this hook must therefore accept the key in its validator (list
 * screens spread `parseListSearch` from `lib/list-search.ts`).
 *
 * Returns a `[value, setValue]` tuple, mirroring `useState`.
 */
/**
 * A `to`-less `useNavigate()` is typed against the *root* route, whose search
 * schema is `{}` — so the generated router narrows a search reducer's return to
 * `never` and rejects any merge. That's the price of being route-agnostic: the
 * typed-router surface can only describe navigation to a route it knows at the
 * call site, and this hook deliberately doesn't know one.
 *
 * The runtime behaviour is correct (a reducer merges into the current route's
 * search), so the seam is narrowed once, here, to the shape we actually use.
 * Every *consumer* stays fully typed, and each route still validates the params
 * it accepts via `validateSearch` — the guarantee that matters is enforced there,
 * not by this signature.
 */
type SearchMergeNavigate = (options: {
  search: (prev: Record<string, unknown>) => Record<string, unknown>
  replace?: boolean
}) => Promise<void>

export function useUrlState(
  key: string,
  fallback = '',
  { push = false }: UrlStateOptions = {},
): readonly [string, (next: string) => void] {
  const search: Record<string, unknown> = useSearch({ strict: false })
  const navigate = useNavigate() as unknown as SearchMergeNavigate

  const raw = search[key]
  const value = typeof raw === 'string' && raw !== '' ? raw : fallback

  const setValue = useCallback(
    (next: string) => {
      void navigate({
        search: (prev) => ({ ...prev, [key]: next || undefined }),
        replace: !push,
      })
    },
    [navigate, key, push],
  )

  return [value, setValue] as const
}
