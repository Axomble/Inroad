// Workspace-invite endpoints. Tagged so the pending-invite list refreshes
// itself after create/revoke — no manual refetch() calls in invites-panel.tsx.
import { api } from '@/store/api'

const teamApi = api.enhanceEndpoints({
  addTagTypes: ['Invite'],
  endpoints: {
    listWorkspaceInvites: {
      providesTags: (result) =>
        result
          ? [
              ...result.map((i) => ({ type: 'Invite' as const, id: i.id ?? 'unknown' })),
              { type: 'Invite' as const, id: 'LIST' },
            ]
          : [{ type: 'Invite' as const, id: 'LIST' }],
    },
    createWorkspaceInvite: {
      invalidatesTags: [{ type: 'Invite', id: 'LIST' }],
    },
    revokeWorkspaceInvite: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Invite', id: arg.inviteId },
        { type: 'Invite', id: 'LIST' },
      ],
    },
  },
})

export const { useListWorkspaceInvitesQuery, useCreateWorkspaceInviteMutation, useRevokeWorkspaceInviteMutation } =
  teamApi
