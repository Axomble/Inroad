import { ActiveSessions } from './active-sessions'
import { PasskeysSettings } from './passkeys-settings'
import { TwoFactorSettings } from './two-factor-settings'

/**
 * Security settings: the self-contained factor panels stack at the top
 * (two-factor, then passkeys — each natural height), with the active-sessions
 * list filling the remaining space and owning its own scroll. Each section owns
 * its server state via RTK Query. The outer column scrolls too, so on short
 * viewports the stacked panels remain reachable.
 */
export function SecurityPage() {
  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <div className="shrink-0">
        <TwoFactorSettings />
      </div>
      <div className="shrink-0">
        <PasskeysSettings />
      </div>
      <div className="min-h-0 flex-1">
        <ActiveSessions />
      </div>
    </div>
  )
}
