import { useCallback, useMemo, useRef } from 'react'
import { useUrlState } from './use-url-state'

/**
 * One named way to order a list. `compare` is a plain comparator so a page can
 * sort on whatever its own row shape exposes without this hook knowing the
 * domain.
 */
export interface SortOption<T> {
  id: string
  label: string
  compare: (a: T, b: T) => number
}

export interface ListControls<T> {
  /** Filtered + sorted items, referentially stable while inputs don't change. */
  items: T[]
  query: string
  setQuery: (query: string) => void
  sortId: string
  setSortId: (id: string) => void
  activeSort: SortOption<T>
  /** True when a query is narrowing the list — drives "no matches" vs "empty". */
  isFiltered: boolean
  /** Count before filtering, for "3 of 24" style copy. */
  totalCount: number
  clear: () => void
}

/**
 * Search + sort for a list that already lives in memory, with the filter and
 * ordering held in the URL (`?q=` / `?sort=`) rather than component state — so a
 * narrowed list is a shareable address and survives a reload. See `useUrlState`
 * for the mechanics and `lib/list-search.ts` for the route validator every list
 * route must spread.
 *
 * The *filtering* is deliberately client-side: every list this app renders today
 * (mailboxes, campaigns, one page of contacts) is already fully loaded in the
 * RTK Query cache, so matching locally is instant and adds no request. When a
 * list outgrows one page, pass `query` into the query arguments instead and drop
 * the `useMemo` — because the state already lives in the URL, nothing else about
 * the component API changes.
 *
 * `searchFields` is a projection to the strings a row should match on, so
 * matching stays explicit rather than stringifying whole objects (which would
 * silently match ids and timestamps).
 *
 * `paramPrefix` disambiguates two independently-filtered lists on one screen.
 *
 * Declare `sorts` at module scope, not inline in the component: the memo that
 * filters and sorts keys off the active comparator's identity, so a fresh array
 * every render would recompute on every render and defeat the point.
 */
export function useListControls<T>({
  items,
  searchFields,
  sorts,
  initialSortId,
  paramPrefix,
}: {
  items: readonly T[]
  searchFields: (item: T) => readonly (string | null | undefined)[]
  sorts: readonly SortOption<T>[]
  initialSortId?: string
  paramPrefix?: string
}): ListControls<T> {
  const fallbackSort = sorts[0]
  if (!fallbackSort) throw new Error('useListControls needs at least one sort option')

  const defaultSortId = initialSortId ?? fallbackSort.id
  const [query, setQuery] = useUrlState(`${paramPrefix ?? ''}q`)
  const [sortParam, setSortId] = useUrlState(`${paramPrefix ?? ''}sort`, defaultSortId)
  // An unrecognised `?sort=` (a stale link, a renamed option) falls back to the
  // default ordering rather than rendering an unsorted list or throwing.
  const sortId = sorts.some((s) => s.id === sortParam) ? sortParam : defaultSortId

  // `searchFields` is almost always an inline arrow, so it has a new identity
  // every render. Holding it in a ref keeps it out of the memo's dependency
  // list without a lint suppression — safe because it is contractually a pure
  // projection, so which render's copy runs cannot change the result.
  const searchFieldsRef = useRef(searchFields)
  searchFieldsRef.current = searchFields

  // No `useMemo` here on purpose: with `sorts` declared at module scope (the
  // documented contract above), `find` returns the *same* option object every
  // render, so the reference `filtered` depends on is already stable. Wrapping
  // it would add a dependency array without changing a single reference.
  const activeSort = sorts.find((s) => s.id === sortId) ?? fallbackSort

  const trimmed = query.trim().toLowerCase()

  // This one is load-bearing: it returns a new array, and that array's identity
  // is what every row's props hang off. Recomputing it on each render would
  // re-render the whole list on every keystroke, and the work is O(n log n) plus
  // a copy — not free. (React 19 does not memoize this for us; that's the
  // opt-in React Compiler, which this build doesn't enable.)
  const filtered = useMemo(() => {
    const base = trimmed
      ? items.filter((item) =>
          searchFieldsRef.current(item).some((field) => field?.toLowerCase().includes(trimmed)),
        )
      : items
    // Copy before sorting: the input is the RTK Query cache array, and
    // Array.prototype.sort mutates in place — sorting it directly would
    // scramble the cached data for every other subscriber.
    return [...base].sort(activeSort.compare)
  }, [items, trimmed, activeSort])

  const clear = useCallback(() => setQuery(''), [setQuery])

  return {
    items: filtered,
    query,
    setQuery,
    sortId,
    setSortId,
    activeSort,
    isFiltered: trimmed.length > 0,
    totalCount: items.length,
    clear,
  }
}

/** Case-insensitive string comparator, for building `SortOption.compare`. */
export function byText<T>(pick: (item: T) => string | null | undefined) {
  return (a: T, b: T) => (pick(a) ?? '').localeCompare(pick(b) ?? '', undefined, { sensitivity: 'base' })
}

/** Descending numeric comparator (largest first), for counts and timestamps. */
export function byNumberDesc<T>(pick: (item: T) => number | null | undefined) {
  return (a: T, b: T) => (pick(b) ?? 0) - (pick(a) ?? 0)
}

/**
 * Comparator that orders by a fixed sequence of known values (e.g. campaign
 * status running → draft → done), with unknown values sorted last.
 */
export function byRank<T>(pick: (item: T) => string | null | undefined, order: readonly string[]) {
  const rank = (item: T) => {
    const index = order.indexOf(pick(item) ?? '')
    return index === -1 ? order.length : index
  }
  return (a: T, b: T) => rank(a) - rank(b)
}
