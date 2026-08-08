/**
 * Query arguments shared by the record screens. They live in one module because
 * RTK Query caches per serialised arg: two readers of the same endpoint only
 * share a cache entry — and so a single request — if they pass the same argument.
 *
 * Notes, tasks, companies and deals are all keyset-paginated (`next_cursor`). The
 * record screens ask for one page at the server's cap and *say so* when more
 * exists — a list that silently stops at the default 50 reads as "that record
 * isn't there". Accumulating the remaining pages needs RTK's `infiniteQuery`,
 * which the OpenAPI codegen does not emit yet.
 */
export const listPageSize = 200
