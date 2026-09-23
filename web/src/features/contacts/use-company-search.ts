import { useEffect, useRef, useState } from 'react'
// Read-only RTK Query hooks from another feature's `api.ts`, the one
// cross-feature import CLAUDE.md allows (hooks only, never UI): the company list
// is CRM's endpoint, and there is nowhere neutral for it to live.
import { useCrmListCompaniesQuery, useLazyCrmListCompaniesQuery, type CrmCompany } from '@/features/crm/api'

/** One page of the picker. Small, because a picker is scanned, not read. */
export const companySearchPageSize = 50

/** The server's floor for `q` (crm.minCompanyQueryLen); below it the API answers 422. */
export const minCompanySearchLength = 2

/**
 * The `q` to send for what the user typed: trimmed, and absent below the floor,
 * so a single character browses the whole workspace instead of provoking a 422.
 * Counted in code points, as the server counts runes — "É" is one character.
 */
export function companySearchTerm(typed: string): string | undefined {
  const term = typed.trim()
  return Array.from(term).length >= minCompanySearchLength ? term : undefined
}

interface LaterPages {
  term: string | undefined
  items: CrmCompany[]
  nextCursor: string | undefined
}

interface LoadMoreFailure {
  term: string | undefined
  error: unknown
}

/**
 * The companies matching `term`, one page at a time.
 *
 * The first page is an ordinary cached query keyed by the term, so typing back
 * to an earlier search is instant and a company created elsewhere invalidates it
 * through the CRM tags. Later pages are appended on demand through the lazy
 * trigger, because the OpenAPI codegen emits no `infiniteQuery`.
 *
 * Every later page is stamped with the term it was fetched for and only shown
 * while that term is current. A slow page for an old search can therefore never
 * be spliced onto a new one — the same reason the server binds its cursor to `q`.
 */
export function useCompanySearch(term: string | undefined) {
  const first = useCrmListCompaniesQuery({ limit: companySearchPageSize, q: term })
  const [fetchPage, pageState] = useLazyCrmListCompaniesQuery()
  const [later, setLater] = useState<LaterPages | null>(null)
  const [failure, setFailure] = useState<LoadMoreFailure | null>(null)

  // The term a resolving request is compared against. A ref, written after
  // render, because the comparison happens in an async continuation that would
  // otherwise close over the term of the render that started it.
  const currentTerm = useRef(term)
  useEffect(() => {
    currentTerm.current = term
  }, [term])

  // `currentData`, not `data`: `data` keeps the PREVIOUS term's page while the
  // new one loads, which would show "acme" results under a search for "glob".
  const firstPage = first.currentData
  const current = later !== null && later.term === term ? later : null
  const items = firstPage ? [...firstPage.items, ...(current?.items ?? [])] : []
  const nextCursor = current ? current.nextCursor : firstPage?.next_cursor

  const loadMore = async () => {
    if (!nextCursor || pageState.isFetching) return
    const requested = term
    setFailure(null)
    // Result-checked rather than unwrapped: unwrap derives a rejecting promise,
    // and this is called fire-and-forget from click and scroll handlers.
    const result = await fetchPage({ limit: companySearchPageSize, q: requested, cursor: nextCursor })
    if (currentTerm.current !== requested) return
    if (result.error !== undefined) {
      setFailure({ term: requested, error: result.error })
      return
    }
    const page = result.data
    if (!page) return
    setLater((previous) => ({
      term: requested,
      items: [...(previous !== null && previous.term === requested ? previous.items : []), ...page.items],
      nextCursor: page.next_cursor,
    }))
  }

  return {
    items,
    loading: first.isFetching && firstPage === undefined,
    error: first.isError ? first.error : undefined,
    retry: () => void first.refetch(),
    hasMore: nextCursor !== undefined,
    loadingMore: pageState.isFetching,
    loadMoreError: failure !== null && failure.term === term ? failure.error : undefined,
    loadMore: () => void loadMore(),
  }
}
