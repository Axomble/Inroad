import { ActiveSessions } from './active-sessions'
import { TwoFactorSettings } from './two-factor-settings'

/**
 * Security settings: two-factor authentication on top (compact, natural height),
 * then the active-sessions list filling the rest with its own scroll. Both are
 * self-contained features that own their own server state via RTK Query.
 */
export function SecurityPage() {
  return (
    <div className="flex h-full flex-col">
      <div className="shrink-0">
        <TwoFactorSettings />
      </div>
      <div className="min-h-0 flex-1">
        <ActiveSessions />
      </div>
    </div>
  )
}
