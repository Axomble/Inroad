import { Link } from '@tanstack/react-router'
import { cn } from '@/lib/utils'

/**
 * Third-level navigation for /app/campaigns/$id/*.
 *
 * A horizontal strip, where the settings equivalent is a vertical rail — the
 * difference is who owns the header. Each /app/settings child renders its own
 * `<Page>` with its own topbar, so a strip there would sit above seven
 * competing titles. Here the LAYOUT owns the one topbar and the stat strip, and
 * the children render only their panels, so tabs read as sections of the page
 * they are already on.
 *
 * Below `sm` it scrolls horizontally rather than wrapping: a wrapped second row
 * would push the campaign's stats off a phone screen, and these five labels are
 * short enough to swipe.
 */
interface CampaignTab {
  label: string
  to: string
}

/**
 * Ordered the way an operator reads a campaign: what it's doing (overview),
 * what it says (steps), who it says it to (leads), when it sends (schedule),
 * and the configuration that shapes every future send (preferences).
 */
const CAMPAIGN_TABS: CampaignTab[] = [
  { label: 'Overview', to: '/app/campaigns/$id' },
  { label: 'Steps', to: '/app/campaigns/$id/steps' },
  { label: 'Leads', to: '/app/campaigns/$id/leads' },
  { label: 'Schedule', to: '/app/campaigns/$id/schedule' },
  { label: 'Preferences', to: '/app/campaigns/$id/preferences' },
]

export function CampaignTabs({ id }: { id: string }) {
  return (
    <nav
      aria-label="Campaign sections"
      data-slot="campaign-tabs"
      className="flex shrink-0 gap-1 overflow-x-auto border-b border-border bg-surface/60 px-3"
    >
      {CAMPAIGN_TABS.map((tab) => (
        <Link
          key={tab.to}
          to={tab.to}
          params={{ id }}
          data-slot="campaign-tab-link"
          // `exact` only for Overview: it is the parent path of every sibling,
          // so without this it would stay active on all five tabs.
          activeOptions={{ exact: tab.to === '/app/campaigns/$id' }}
          className={cn(
            'relative shrink-0 whitespace-nowrap px-3 py-2.5 text-[13px] text-muted-foreground transition-colors',
            'hover:text-foreground',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-inset',
          )}
          activeProps={{
            // The underline is drawn with a pseudo-element rather than a border
            // so it overlaps the nav's own bottom border instead of stacking a
            // second line beneath it.
            className: cn(
              'font-medium text-foreground',
              'after:absolute after:inset-x-3 after:-bottom-px after:h-0.5 after:bg-primary after:content-[""]',
            ),
          }}
        >
          {tab.label}
        </Link>
      ))}
    </nav>
  )
}
