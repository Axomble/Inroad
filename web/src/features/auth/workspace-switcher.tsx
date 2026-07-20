import { Building2, Check, ChevronsUpDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { setActiveWorkspace } from '@/store/slices/auth'
import { api } from '@/store/api'
import { useAuthSwitchWorkspaceMutation } from './api'

/**
 * Workspace picker for the app header: lists the current user's memberships
 * (from the `auth` slice), highlights the active one, and swaps the active
 * workspace via `POST /auth/switch-workspace` on selection. The response
 * carries a workspace-scoped access token + role, so on success we replace
 * the in-memory session (`setActiveWorkspace`) and reset the RTK Query cache
 * (`api.util.resetApiState()`) so every workspace-scoped query refetches
 * against the new workspace instead of serving stale cached data.
 */
export function WorkspaceSwitcher() {
  const memberships = useAppSelector((state) => state.auth.memberships)
  const activeWorkspaceId = useAppSelector((state) => state.auth.activeWorkspaceId)
  const dispatch = useAppDispatch()
  const [switchWorkspace, { isLoading }] = useAuthSwitchWorkspaceMutation()

  const active = memberships.find((m) => m.workspace_id === activeWorkspaceId)

  // Before bootstrap resolves (or for a session with no memberships yet) render
  // an inert placeholder rather than an empty/crashing dropdown.
  if (memberships.length === 0) {
    return (
      <span className="flex items-center gap-1.5 px-2 text-[13px] font-medium text-muted-foreground">
        <Building2 className="size-4" />
        No workspace
      </span>
    )
  }

  async function handleSelect(workspaceId: string) {
    if (workspaceId === activeWorkspaceId || isLoading) return
    const result = await switchWorkspace({ switchWorkspaceRequest: { workspace_id: workspaceId } })
    if ('data' in result && result.data) {
      // Abort every RTK Query request that was in flight against the previous
      // workspace before flipping the active token. Otherwise a slow response
      // can land after `setActiveWorkspace`, get parsed against the new
      // workspace's cache, and briefly show old data (or crash a component
      // expecting new-shape data). `getRunningQueriesThunk` returns undefined
      // when nothing is subscribed — guard both branches.
      const runningQueries = dispatch(api.util.getRunningQueriesThunk())
      const runningMutations = dispatch(api.util.getRunningMutationsThunk())
      for (const q of runningQueries ?? []) q.abort()
      for (const m of runningMutations ?? []) m.abort()

      dispatch(
        setActiveWorkspace({
          activeWorkspaceId: result.data.active_workspace_id,
          role: result.data.role,
          accessToken: result.data.access_token,
        }),
      )
      // Workspace-scoped data (mailboxes, campaigns, ...) is no longer valid
      // for the new active workspace — drop every cached query/mutation result.
      dispatch(api.util.resetApiState())
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="gap-1.5 px-2 font-semibold"
          disabled={isLoading}
          aria-label="Switch workspace"
        >
          <Building2 className="text-muted-foreground" />
          <span className="max-w-40 truncate">{active?.workspace_name ?? 'Select workspace'}</span>
          <ChevronsUpDown className="text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-56">
        <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {memberships.map((membership) => (
          <DropdownMenuItem
            key={membership.workspace_id}
            onSelect={() => void handleSelect(membership.workspace_id)}
          >
            <span className="flex-1 truncate">{membership.workspace_name}</span>
            {membership.workspace_id === activeWorkspaceId && <Check className="size-4" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
