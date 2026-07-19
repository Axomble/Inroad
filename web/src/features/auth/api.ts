// Re-export generated auth hooks so features import from their own folder.
export {
  useAuthRegisterMutation,
  useAuthLoginMutation,
  useAuthRefreshMutation,
  useAuthLogoutMutation,
  useAuthMeQuery,
  useAuthLogoutAllMutation,
  useAuthSwitchWorkspaceMutation,
} from '../../store/api'
