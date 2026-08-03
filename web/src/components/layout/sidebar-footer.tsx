import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Skeleton } from '@/components/ui/skeleton'
import { initialsFromIdentity } from '@/lib/initials'
import { useAppSelector } from '@/store/hooks'

/**
 * Identity footer pinned to the bottom of the sidebar (spec §11): avatar +
 * name + email, so who-am-I lives in the chrome instead of hiding behind the
 * header's initials-only trigger. Reads auth *state* directly — the same
 * hook-level seam `AppSidebar` already uses for the role — never feature UI.
 *
 * Until the session settles (hard reload restores it asynchronously) the
 * footer reserves its exact height with neutral skeletons, so the sidebar
 * never jumps and never flashes a wrong-looking identity.
 */
export function SidebarFooter() {
  const userName = useAppSelector((s) => s.auth.userName)
  const userEmail = useAppSelector((s) => s.auth.userEmail)
  const initials = initialsFromIdentity(userName, userEmail)
  const displayName = userName?.trim() || userEmail

  return (
    <div data-slot="sidebar-footer" className="mt-auto flex items-center gap-2.5 border-t border-chrome-border px-2.5 pt-3">
      {initials ? (
        <Avatar className="size-7">
          <AvatarFallback className="text-[10px]">{initials}</AvatarFallback>
        </Avatar>
      ) : (
        <Skeleton className="size-7 shrink-0 bg-chrome-surface" />
      )}
      <div className="flex min-w-0 flex-col gap-0.5 leading-tight">
        {displayName ? (
          <>
            <p className="truncate text-[12.5px] font-medium text-chrome-text">{displayName}</p>
            {userEmail && userEmail !== displayName && (
              <p className="truncate font-mono text-[10px] text-chrome-muted">{userEmail}</p>
            )}
          </>
        ) : (
          <>
            <Skeleton className="h-3 w-24 bg-chrome-surface" />
            <Skeleton className="h-2.5 w-32 bg-chrome-surface" />
          </>
        )}
      </div>
    </div>
  )
}
