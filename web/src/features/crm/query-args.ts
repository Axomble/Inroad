/**
 * CRM query arguments. Shared here because RTK Query caches per serialised arg:
 * two readers of the same endpoint only share a cache entry — and so a single
 * request, and a single optimistic patch — if they pass the same argument.
 *
 * The page-size cap is not CRM-specific and lives in
 * `@/features/records/query-args`.
 */

/**
 * The board endpoint takes an optional `pipelineId`; every reader shows the
 * default pipeline, so one shared constant arg keeps the board and the deals
 * page's stat strip on a single cache entry.
 */
export const defaultBoardArg = {}
