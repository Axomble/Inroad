import { useState } from 'react'
import { useSearch } from '@tanstack/react-router'
import { RefreshCw } from 'lucide-react'
import { EmptyBlock, Page, PageBody, PageTopbar } from '@/components/layout/page'
import { QueryErrorBanner } from '@/components/shared/record-page'
import { Button } from '@/components/ui/button'
import { useHasRole } from '@/hooks/use-has-role'
import { useUrlPatch } from '@/hooks/use-url-state'
import { AuditEventFeed } from './audit-event-feed'
import {
  ADMINS_ONLY_DESCRIPTION,
  ADMINS_ONLY_TITLE,
  EMPTY_DESCRIPTION,
  EMPTY_FILTERED_DESCRIPTION,
  EMPTY_FILTERED_TITLE,
  EMPTY_TITLE,
  INVERTED_RANGE,
  PAGE_INTRO,
} from './audit-log-copy'
import { AuditLogFilters } from './audit-log-filters'
import { auditFilterArgs, CLEAR_FILTERS, hasFilters, isRangeInverted, parseAuditLogSearch } from './audit-log-search'

const TITLE = 'Audit log'
const SUBTITLE = 'Sign-ins, access and changes'

/**
 * The workspace audit log.
 *
 * OWNER/ADMIN ONLY, mirroring `auditlog.Handler.Routes`, which wraps the router in
 * `RequireRole("admin")`. This check is the courtesy half — the server is the
 * boundary — so a member deep-linking here gets an honest state rather than a
 * request that can only come back 403. A 403 that arrives anyway (a role changed
 * mid-session) is still rendered, through the feed's error copy.
 */
export function AuditLogPage() {
  const isAdmin = useHasRole('admin')

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Settings" title={TITLE} subtitle={SUBTITLE} />
        <EmptyBlock title={ADMINS_ONLY_TITLE} description={ADMINS_ONLY_DESCRIPTION} />
      </Page>
    )
  }
  return <AuditLogView />
}

function AuditLogView() {
  const search = parseAuditLogSearch(useSearch({ strict: false }))
  const patch = useUrlPatch()
  // Bumped by Refresh. It remounts the feed, which drops every loaded page after
  // the first: appending fresh events above cursors minted before them would leave
  // a gap exactly the size of what just happened.
  const [generation, setGeneration] = useState(0)
  const filters = auditFilterArgs(search)

  return (
    <Page>
      <PageTopbar
        eyebrow="Settings"
        title={TITLE}
        subtitle={SUBTITLE}
        actions={
          <Button variant="outline" size="sm" onClick={() => setGeneration((n) => n + 1)}>
            <RefreshCw aria-hidden="true" />
            Refresh
          </Button>
        }
      />
      <PageBody>
        <p className="max-w-prose px-4 pt-3 text-[12px] leading-snug text-muted-foreground sm:px-5">{PAGE_INTRO}</p>
        <AuditLogFilters search={search} onChange={patch} />

        {isRangeInverted(search) ? (
          // Held back rather than sent: the server would answer a 400, and the reader
          // needs to know which control to fix, not that "the server refused".
          <QueryErrorBanner message={INVERTED_RANGE} className="mx-4 my-3 sm:mx-5" />
        ) : (
          <AuditEventFeed
            // The filter set IS the feed's identity: a new one must start from page one.
            key={`${JSON.stringify(filters)}#${generation}`}
            filters={filters}
            empty={
              hasFilters(search)
                ? {
                    title: EMPTY_FILTERED_TITLE,
                    description: EMPTY_FILTERED_DESCRIPTION,
                    action: (
                      <Button variant="outline" size="sm" onClick={() => patch(CLEAR_FILTERS)}>
                        Clear filters
                      </Button>
                    ),
                  }
                : { title: EMPTY_TITLE, description: EMPTY_DESCRIPTION }
            }
          />
        )}
      </PageBody>
    </Page>
  )
}
