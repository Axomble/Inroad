import { AlertCircle } from 'lucide-react'
import { EmptyBlock, Page, PageBody, PageTopbar, SectionBar } from '@/components/layout/page'
import { Skeleton } from '@/components/ui/skeleton'
import { useHasRole } from '@/hooks/use-has-role'
import { cn } from '@/lib/utils'
import { useListFleetWorkersQuery } from './api'
import { FleetWorkerRow } from './fleet-worker-row'
import { MailboxDecisionsPanel } from './mailbox-decisions-panel'
import { ScheduledJobsPanel } from './scheduled-jobs-panel'
import {
  ADMINS_ONLY_DESCRIPTION,
  ADMINS_ONLY_TITLE,
  EMPTY_FLEET_DESCRIPTION,
  EMPTY_FLEET_TITLE,
  fleetErrorMessage,
  PAGE_INTRO,
  SHARED_FATE_NOTICE,
} from './fleet-copy'

/**
 * How far back to aggregate. A client choice, NOT a copy of a server policy
 * constant: the server clamps to its own floor and ceiling and echoes what it
 * actually used, and every heading on this screen reads that echo rather than
 * this number.
 *
 * 24 hours because the question is "how is this address being treated now" and
 * a mailbox provider's patience is measured in hours; a week would average a
 * worker that started failing this morning back into looking fine.
 */
const WINDOW_HOURS = 24

/**
 * The fleet operator view.
 *
 * This exists because three merged changes collect fleet data that nothing ever
 * read. Workers have been heartbeating their egress IPs, providers' verdicts
 * have been counted per address, and every placement decision has been recorded
 * with the prose explaining it — all of it visible only from psql. An operator
 * cannot act on a worker whose provider has started refusing its credentials if
 * nothing shows them that it has.
 *
 * ADMIN-GATED, mirroring `fleet.Handler.Routes`, which wraps the whole router in
 * `RequireRole("admin")` inside the session-only mount group. Worker ids and
 * egress IPs are deployment infrastructure: no API key and no OAuth grant can
 * reach that surface at all, and a member session is refused. This gate is the
 * courtesy half — the server is the boundary — so a non-admin deep-linking here
 * gets an honest state instead of three "couldn't load" banners over three 403s.
 */
export function FleetPage() {
  const isAdmin = useHasRole('admin')

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Settings" title="Fleet" subtitle="Workers, placement and sweeps" />
        <EmptyBlock title={ADMINS_ONLY_TITLE} description={ADMINS_ONLY_DESCRIPTION} />
      </Page>
    )
  }

  return (
    <Page>
      <PageTopbar eyebrow="Settings" title="Fleet" subtitle="Workers, placement and sweeps" />
      <PageBody>
        <WorkersSection />
        <ScheduledJobsPanel windowHours={WINDOW_HOURS} />
        <MailboxDecisionsPanel />
      </PageBody>
    </Page>
  )
}

function WorkersSection() {
  const { data, isLoading, isError, error } = useListFleetWorkersQuery({ windowHours: WINDOW_HOURS })
  const workers = data?.workers ?? []
  // From the response, not from WINDOW_HOURS: the server clamps, and a heading
  // that repeated the request would eventually name a window nobody measured.
  const effectiveWindow = data?.window_hours ?? WINDOW_HOURS

  return (
    <section data-slot="fleet-workers">
      <SectionBar
        label="Workers"
        count={isLoading || isError ? undefined : workers.length}
        className={cn(isError && 'border-danger/30')}
      >
        {!isLoading && !isError && (
          <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">
            last {effectiveWindow}h
          </span>
        )}
      </SectionBar>

      <p className="max-w-prose px-4 pt-3 text-[12px] leading-snug text-muted-foreground sm:px-5">{PAGE_INTRO}</p>

      {isError ? (
        /*
          The banner replaces the list rather than sitting above an empty one. An
          empty fleet is an ordinary state here, so "nothing is pinned" under a
          failed read would tell an operator their mail is fine when nothing at
          all is known.
        */
        <div
          role="alert"
          data-slot="fleet-workers-error"
          className="flex items-start gap-2 px-4 py-4 text-[12px] leading-snug text-danger sm:px-5"
        >
          <AlertCircle className="mt-px size-4 shrink-0" aria-hidden="true" />
          <span>{fleetErrorMessage(error, "Couldn't load the fleet. Refresh the page to try again.")}</span>
        </div>
      ) : isLoading ? (
        <WorkerSkeleton />
      ) : workers.length === 0 ? (
        <EmptyBlock title={EMPTY_FLEET_TITLE} description={EMPTY_FLEET_DESCRIPTION} />
      ) : (
        <>
          {/*
            Said once, above the rows, rather than on each: these counters are
            per address and a worker can carry several workspaces' mailboxes, so
            they are not a count of this workspace's own traffic. Omitting it
            would let an operator read another tenant's throttling as their own
            sending problem.
          */}
          <p
            data-slot="fleet-shared-fate"
            className="max-w-prose px-4 pb-1 pt-2 text-[12px] leading-snug text-faint sm:px-5"
          >
            {SHARED_FATE_NOTICE}
          </p>
          <ul>
            {workers.map((worker) => (
              <FleetWorkerRow key={worker.worker_id} worker={worker} />
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

function WorkerSkeleton() {
  return (
    <ul>
      {[0, 1].map((i) => (
        <li key={i} className="border-b border-border px-4 py-3 sm:px-5">
          <div className="space-y-2">
            <Skeleton className="h-3.5 w-52" />
            <Skeleton className="h-2.5 w-72" />
            <Skeleton className="h-2.5 w-64" />
          </div>
        </li>
      ))}
    </ul>
  )
}
