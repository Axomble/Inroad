import { lazy, Suspense } from 'react'
import { useAppSelector } from '@/store/hooks'
import { useAuthMeQuery } from './api'

// The panel pulls in react-hook-form, zod and Radix's dialog, and it renders once
// in a workspace's lifetime — so it is fetched only when it's actually needed,
// rather than weighing down the chunk every authenticated route loads.
const OnboardingDialog = lazy(() => import('./workspace-onboarding-dialog'))

/**
 * The mandatory first-run step, mounted for every authenticated route: a
 * workspace that has never been named blocks the app until it is.
 *
 * Naming is the ONLY thing this asks for. Inviting teammates and connecting a
 * mailbox are first-class features in Settings, not a toll on the way in — a
 * signup flow that demands them makes the product feel like it's for one person
 * warming one mailbox.
 *
 * Whether it shows is decided by the ACTIVE WORKSPACE's own flag on `/auth/me`,
 * never by "did this user just register": someone invited into an already-named
 * workspace joins straight into the app.
 *
 * The check is `=== null` rather than a falsiness test on purpose. An API that
 * predates onboarding sends no such field, and `undefined` must not open an
 * un-dismissible screen in front of every existing user — the two failure modes are
 * not symmetric, so only an explicit `null` ("this workspace exists and has never
 * been named") opens it.
 */
export function WorkspaceOnboardingOverlay() {
  const authed = useAppSelector((state) => state.auth.status === 'authed')
  const { data } = useAuthMeQuery(undefined, { skip: !authed })
  const activeWorkspaceId = useAppSelector((state) => state.auth.activeWorkspaceId)
  const memberships = useAppSelector((state) => state.auth.memberships)

  if (data?.onboarding_completed_at !== null) return null

  // The server named the workspace from the Google account's domain when it
  // created it, and that name arrives on the session's membership list — so the
  // common case is confirming a sensible default, not typing from scratch.
  const derivedName = memberships.find((m) => m.workspace_id === activeWorkspaceId)?.workspace_name ?? ''

  return (
    // No fallback: the app behind this is already rendered and usable-looking, and
    // a flash of skeleton over it would read as a broken screen. The panel appears
    // when its chunk lands, a tick later.
    <Suspense fallback={null}>
      <OnboardingDialog
        workspaceId={data.active_workspace_id}
        derivedName={derivedName}
        email={data.email}
      />
    </Suspense>
  )
}
