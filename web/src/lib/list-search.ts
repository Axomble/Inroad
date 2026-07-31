/**
 * The search-param contract every list screen shares: `?q=` for the filter text
 * and `?sort=` for the active ordering.
 *
 * These live in the URL rather than component state so a filtered list is a
 * shareable, bookmarkable, back-button-able address — "the three at-risk
 * mailboxes" is a link you can paste to a teammate, and reloading the page keeps
 * you where you were.
 *
 * TanStack `validateSearch` is authoritative: a param a route's validator does
 * not return is stripped from the URL. So every list route must spread
 * `parseListSearch` into its validator, or `q`/`sort` would be silently dropped
 * on the first navigation.
 */
export interface ListSearch {
  q?: string
  sort?: string
}

/** Narrow untrusted URL input to the list-search contract. */
export function parseListSearch(search: Record<string, unknown>): ListSearch {
  return {
    // An empty string is normalised away so `?q=` never lingers in the URL
    // after the user clears the box.
    q: typeof search.q === 'string' && search.q !== '' ? search.q : undefined,
    sort: typeof search.sort === 'string' && search.sort !== '' ? search.sort : undefined,
  }
}
