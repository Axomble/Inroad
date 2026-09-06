// Notes, tasks and activity — the attachments every record type shares.
//
// They live in their own feature because the API models them polymorphically:
// `note_targets` / `task_targets` each carry a nullable contact/company/deal id,
// so one note endpoint serves all three record types. They were only ever in
// `features/crm` because that is where records happened to land first, which is
// how the contacts screens ended up importing CRM UI.
//
// The generated `store/api.ts` already declares every endpoint from
// `api/openapi.yaml`; this module only layers cache tags on top via
// `enhanceEndpoints`. Nothing is injected here — a hand-injected endpoint hitting
// the same URL is a *different* cache entry, so a write through one is invisible
// to the other, and because the names differ RTK never warns.
import { api } from '@/store/api'

// One source of truth for shapes: re-export the generated definitions rather than
// restating them. The `Crm` prefix is the code generator's, taken from the
// OpenAPI operation ids; the shapes themselves are record-generic.
export type { CrmEvent, CrmNote, CrmTask, CrmTaskInput, CrmTargetFields } from '@/store/api'

import type { CrmTargetFields } from '@/store/api'

/**
 * Which record a note, task or event hangs off. This union is what lets contacts,
 * companies and deals share one set of panels.
 */
export type CrmTargetType = CrmTargetFields['target_type']

const recordsApi = api.enhanceEndpoints({
  addTagTypes: ['RecordActivity', 'RecordTask'],
  endpoints: {
    crmListEvents: {
      providesTags: (_result, _error, { targetId }) => [{ type: 'RecordActivity', id: targetId }],
    },

    crmListNotes: {
      providesTags: (_result, _error, { targetId }) => [{ type: 'RecordActivity', id: targetId }],
    },
    crmCreateNote: {
      invalidatesTags: (_result, _error, { crmNoteInput }) => [
        { type: 'RecordActivity', id: crmNoteInput.target_id },
      ],
    },
    // Note update/delete take an id only — the target is unknown here, so the
    // whole activity tag family is refetched rather than guessing wrong.
    crmUpdateNote: {
      invalidatesTags: ['RecordActivity'],
    },
    crmDeleteNote: {
      invalidatesTags: ['RecordActivity'],
    },

    crmListTasks: {
      providesTags: (_result, _error, { targetId }) => [{ type: 'RecordTask', id: targetId }],
    },
    crmCreateTask: {
      invalidatesTags: (_result, _error, { crmTaskInput }) => [
        { type: 'RecordTask', id: crmTaskInput.target_id },
        { type: 'RecordActivity', id: crmTaskInput.target_id },
      ],
    },
    crmUpdateTask: {
      invalidatesTags: (_result, _error, { crmTaskInput }) => [
        { type: 'RecordTask', id: crmTaskInput.target_id },
        { type: 'RecordActivity', id: crmTaskInput.target_id },
      ],
    },
    crmDeleteTask: {
      invalidatesTags: ['RecordTask', 'RecordActivity'],
    },
  },
})

export const {
  useCrmListEventsQuery,
  useCrmListNotesQuery,
  useCrmCreateNoteMutation,
  useCrmListTasksQuery,
  useCrmCreateTaskMutation,
  useCrmUpdateTaskMutation,
  useCrmDeleteTaskMutation,
} = recordsApi
