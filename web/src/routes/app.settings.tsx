import { createFileRoute, Outlet } from '@tanstack/react-router'
import { SettingsRail } from '@/components/layout/settings-rail'

/**
 * Layout for every /app/settings/* screen: the settings rail beside whatever
 * child route is active. Each child still renders its own `<Page>`, so this
 * layout owns only the second-level navigation and the flex frame around it.
 *
 * Column direction flips at `sm` to match the rail, which is a top strip on a
 * phone and a left rail above it.
 */
export const Route = createFileRoute('/app/settings')({
  component: SettingsLayout,
})

function SettingsLayout() {
  return (
    <div className="flex h-full flex-col sm:flex-row">
      <SettingsRail />
      {/* min-w-0 so a wide child (an API-key table, a long provider name)
          scrolls inside itself rather than pushing the rail off-screen. */}
      <div className="min-h-0 min-w-0 flex-1">
        <Outlet />
      </div>
    </div>
  )
}
