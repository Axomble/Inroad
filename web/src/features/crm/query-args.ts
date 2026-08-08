/**
 * Query arguments shared across the CRM screens. They live in one module because
 * RTK Query caches per serialised arg: two readers of the same endpoint only
 * share a cache entry — and so a single request, and a single optimistic patch —
 * if they pass the same argument.
 */

/**
 * Companies, deals, notes and tasks are keyset-paginated (`next_cursor`). The
 * CRM screens ask for one page at the server's cap and *say so* when more
 * exists — a list that silently stops at the default 50 reads as "that record
 * isn't there". Accumulating the remaining pages needs RTK's `infiniteQuery`,
 * which the OpenAPI codegen does not emit yet.
 */
export const listPageSize = 200

/**
 * The board endpoint takes an optional `pipelineId`; every reader shows the
 * default pipeline, so one shared constant arg keeps the board and the deals
 * page's stat strip on a single cache entry.
 */
export const defaultBoardArg = {}
