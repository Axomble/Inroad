// Inbox feature endpoints. The four operations are already fully generated
// from api/openapi.yaml into store/api.ts (listInboxThreads, getInboxThread,
// sendInboxReply, setInboxThreadRead) — this file only layers cache tags on
// top via `enhanceEndpoints` (so marking a thread read/unread, or sending a
// reply, refetches the list without a manual `refetch()` at every call site),
// injects the one shape codegen cannot emit (the search's infinite query), and
// re-exports the hooks under this feature's own import surface, matching
// every other feature's `api.ts` (see features/mailboxes/api.ts,
// features/campaigns/api.ts).
import { api, type InboxSearchPage, type SearchInboxThreadsApiArg } from '@/store/api'

export type {
  InboxSearchHit,
  InboxSearchPage,
  InboxSearchSnippet,
  SearchInboxThreadsApiArg,
  InboxThreadSummary,
  InboxThreadDetail,
  InboxMessage,
  InboxReplyLabelRef,
  InboxDraftReply,
  InboxOverview,
  InboxSnooze,
  InboxLabel,
  InboxPendingReply,
  InboxSettings,
  InboxComposeDraft,
  InboxPendingCompose,
  InboxMailboxCount,
  InboxReplyClassCount,
  SendInboxReplyRequest,
  SetInboxThreadReadRequest,
  ListInboxThreadsApiArg,
} from '@/store/api'

const inboxApi = api.enhanceEndpoints({
  addTagTypes: ['InboxThread', 'InboxLabel', 'InboxOutbox', 'InboxDraft'],
  endpoints: {
    listInboxThreads: {
      providesTags: (result) =>
        result
          ? [
              ...result.items.map((t) => ({ type: 'InboxThread' as const, id: t.id })),
              { type: 'InboxThread' as const, id: 'LIST' },
            ]
          : [{ type: 'InboxThread' as const, id: 'LIST' }],
    },
    getInboxThread: {
      providesTags: (_result, _error, arg) => [{ type: 'InboxThread', id: arg.id }],
    },
    // The overview's counts describe the same rows the list serves, so they go
    // stale on exactly the same events. Tagging it with LIST means marking a
    // thread read (or sending a reply) refreshes the rail's counters through
    // the invalidations those mutations already declare — no separate tag to
    // keep in step.
    getInboxOverview: {
      providesTags: [{ type: 'InboxThread', id: 'LIST' }],
    },
    // Invalidates the LIST tag too: an unread thread marked read must
    // disappear from an "unread" filter (none exists yet, but the same tag
    // also drives the rail's per-mailbox counts, which must stay in step).
    setInboxThreadRead: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread', id: arg.id },
        { type: 'InboxThread', id: 'LIST' },
      ],
    },
    // Snoozing moves a thread between scopes and changes every rail counter
    // that excludes snoozed threads, so it invalidates the LIST as well as the
    // thread itself — the same pair setInboxThreadRead invalidates, for the
    // same reason.
    snoozeInboxThread: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread', id: arg.id },
        { type: 'InboxThread', id: 'LIST' },
      ],
    },
    unsnoozeInboxThread: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread', id: arg.id },
        { type: 'InboxThread', id: 'LIST' },
      ],
    },
    // Labels: the taxonomy itself and the per-thread assignments live under
    // different tags. Creating or renaming a label changes the PICKER
    // (InboxLabel), while applying one to a thread changes that THREAD and the
    // rail counts (InboxThread). Conflating them would refetch every thread
    // whenever someone recoloured a label.
    listInboxLabels: {
      providesTags: [{ type: 'InboxLabel' as const, id: 'LIST' }],
    },
    createInboxLabel: {
      invalidatesTags: [{ type: 'InboxLabel' as const, id: 'LIST' }],
    },
    updateInboxLabel: {
      // Also invalidates threads: a renamed or recoloured label is embedded in
      // every thread carrying it, so the chips would otherwise show stale copy.
      invalidatesTags: [
        { type: 'InboxLabel' as const, id: 'LIST' },
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    },
    deleteInboxLabel: {
      invalidatesTags: [
        { type: 'InboxLabel' as const, id: 'LIST' },
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    },
    assignInboxThreadLabel: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread' as const, id: arg.id },
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    },
    unassignInboxThreadLabel: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread' as const, id: arg.id },
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    },
    // The outbox and the thread both hold a queued reply's state, so scheduling
    // or cancelling one invalidates both. The thread tag is what makes the
    // reader's countdown appear and disappear without a manual refetch.
    listInboxOutbox: {
      providesTags: [{ type: 'InboxOutbox' as const, id: 'LIST' }],
    },
    scheduleInboxReply: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxOutbox' as const, id: 'LIST' },
        { type: 'InboxThread' as const, id: arg.id },
      ],
    },
    cancelInboxPendingReply: {
      // No thread tag available here — the arg carries only the pending id, not
      // the thread's. LIST covers the reader too, since the thread list and the
      // reader share it.
      invalidatesTags: [
        { type: 'InboxOutbox' as const, id: 'LIST' },
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    },
    // Drafts are their own tag: autosave fires on a debounce while typing, and
    // folding it into InboxOutbox would refetch the outbox on every keystroke.
    listInboxComposeDrafts: {
      providesTags: [{ type: 'InboxDraft' as const, id: 'LIST' }],
    },
    saveInboxComposeDraft: {
      // Deliberately NO invalidation. Autosave already holds the authoritative
      // draft in the composer's own state, and invalidating the list on every
      // debounced keystroke would refetch continuously for a list the operator
      // is not looking at.
    },
    deleteInboxComposeDraft: {
      invalidatesTags: [{ type: 'InboxDraft' as const, id: 'LIST' }],
    },
    listInboxComposes: {
      providesTags: [{ type: 'InboxOutbox' as const, id: 'LIST' }],
    },
    sendInboxCompose: {
      // Both: the compose leaves the drafts list and joins the outbox.
      invalidatesTags: [
        { type: 'InboxOutbox' as const, id: 'LIST' },
        { type: 'InboxDraft' as const, id: 'LIST' },
      ],
    },
    cancelInboxPendingCompose: {
      invalidatesTags: [{ type: 'InboxOutbox' as const, id: 'LIST' }],
    },
    getInboxSettings: {
      providesTags: [{ type: 'InboxOutbox' as const, id: 'SETTINGS' }],
    },
    updateInboxSettings: {
      invalidatesTags: [{ type: 'InboxOutbox' as const, id: 'SETTINGS' }],
    },
    // The send is queued (202), not delivered — the outbound message doesn't
    // exist yet at the instant this resolves, so this immediate invalidation
    // is a best-effort refetch, not a guarantee the reply is visible yet. The
    // composer schedules one bounded delayed refetch of its own to cover that
    // gap once the worker has had a moment to land the message.
    sendInboxReply: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread', id: arg.id },
        { type: 'InboxThread', id: 'LIST' },
      ],
    },
  },
}).injectEndpoints({
  endpoints: (build) => ({
    // The search box's "Load more" list, as an RTK Query infinite query over
    // the same GET /inbox/search the generated `searchInboxThreads` calls. The
    // generated endpoint is a plain query — one page per cache entry — so
    // accumulating pages behind "Load more" needs this shape instead, and a
    // distinct name (the generated one already claims `searchInboxThreads`,
    // and an injection under a claimed name silently no-ops). Arg and page
    // types are the generated ones; only the cursor moves from the arg to the
    // page param. An invalidation (a thread marked read) refetches every
    // loaded page, not just the first, so rows never duplicate or vanish.
    searchInboxThreadPages: build.infiniteQuery<InboxSearchPage, Omit<SearchInboxThreadsApiArg, 'cursor'>, string>({
      infiniteQueryOptions: {
        // '' is "no cursor": the first page. The API's next_cursor is null on
        // the last page, which is what ends `hasNextPage`.
        initialPageParam: '',
        getNextPageParam: (lastPage) => lastPage.next_cursor,
      },
      query: ({ queryArg, pageParam }) => ({
        url: `/inbox/search`,
        params: {
          q: queryArg.q,
          cursor: pageParam || undefined,
          limit: queryArg.limit,
          scope: queryArg.scope,
          tz_offset: queryArg.tzOffset,
          mailbox_id: queryArg.mailboxId,
          reply_class: queryArg.replyClass,
          label: queryArg.label,
        },
      }),
      providesTags: (result) => [
        ...(result?.pages ?? []).flatMap((page) =>
          page.items.map((hit) => ({ type: 'InboxThread' as const, id: hit.thread.id })),
        ),
        { type: 'InboxThread' as const, id: 'LIST' },
      ],
    }),
  }),
})

// `draftInboxReply` is generated now (the interim injection this file carried
// under a TODO(codegen) has been deleted — the generated endpoint claims the
// name, which would have silently no-op'd the injection anyway). It stays
// without `invalidatesTags`, deliberately: drafting reads the thread and
// returns generated text, it persists nothing. Invalidating `InboxThread`
// would refetch the thread (and the list) for a server state that did not
// change, and the refetch would land while the user is editing the draft in
// the textarea — cost with no correctness gain.
export const {
  useListInboxThreadsQuery,
  useSearchInboxThreadPagesInfiniteQuery,
  useGetInboxOverviewQuery,
  useGetInboxThreadQuery,
  useSendInboxReplyMutation,
  useDraftInboxReplyMutation,
  useSetInboxThreadReadMutation,
  useSnoozeInboxThreadMutation,
  useUnsnoozeInboxThreadMutation,
  useListInboxLabelsQuery,
  useCreateInboxLabelMutation,
  useUpdateInboxLabelMutation,
  useDeleteInboxLabelMutation,
  useAssignInboxThreadLabelMutation,
  useUnassignInboxThreadLabelMutation,
  useListInboxOutboxQuery,
  useScheduleInboxReplyMutation,
  useCancelInboxPendingReplyMutation,
  useGetInboxSettingsQuery,
  useUpdateInboxSettingsMutation,
  useListInboxComposeDraftsQuery,
  useSaveInboxComposeDraftMutation,
  useDeleteInboxComposeDraftMutation,
  useListInboxComposesQuery,
  useSendInboxComposeMutation,
  useCancelInboxPendingComposeMutation,
} = inboxApi
