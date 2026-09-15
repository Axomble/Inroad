import { StatusDot, type StatusTone } from '@/components/shared/status-pill'
import { EmptyBlock, SectionBar } from '@/components/layout/page'
import { Skeleton } from '@/components/ui/skeleton'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import { useListScheduledJobsQuery, type ScheduledJob } from './api'
import {
  fleetErrorMessage,
  formatCount,
  formatDuration,
  JOB_ERROR_TEXT_WITHHELD,
  JOBS_EMPTY_DESCRIPTION,
  JOBS_EMPTY_TITLE,
  JOBS_INTRO,
  jobStatusCopy,
} from './fleet-copy'

const TONE: Record<'ok' | 'warn' | 'bad', StatusTone> = {
  ok: 'running',
  warn: 'warming',
  bad: 'failing',
}

/**
 * Whether the periodic sweeps are actually running.
 *
 * The ledger behind this had an insert and a retention purge and no read query
 * at all, so "did last night's domain-auth sweep run" was answerable only by
 * opening a psql session. This is the query's first reader.
 *
 * `window_hours` is echoed by the server because the requested value is
 * clamped, and the heading takes it from the response rather than from the
 * request — a screen that labelled its own column would eventually label a
 * 24-hour rollup with a number nobody measured.
 */
export function ScheduledJobsPanel({ windowHours }: { windowHours: number }) {
  const { data, isLoading, isError, error } = useListScheduledJobsQuery({ windowHours })
  const jobs = data?.jobs ?? []

  return (
    <section data-slot="fleet-jobs">
      <SectionBar label="Scheduled sweeps" count={isLoading || isError ? undefined : jobs.length} />
      <p className="max-w-prose px-4 pt-3 text-[12px] leading-snug text-muted-foreground sm:px-5">{JOBS_INTRO}</p>

      {isLoading ? (
        <JobSkeleton />
      ) : isError ? (
        /*
          Nothing below the message. An empty list under a failed read would say
          "no sweep has reported", which is the one thing this panel must never
          claim when it does not know.
        */
        <p
          role="alert"
          data-slot="fleet-jobs-error"
          className="px-4 py-4 text-[12px] leading-snug text-danger sm:px-5"
        >
          {fleetErrorMessage(error, "Couldn't load the scheduled sweeps. Refresh the page to try again.")}
        </p>
      ) : jobs.length === 0 ? (
        <EmptyBlock title={JOBS_EMPTY_TITLE} description={JOBS_EMPTY_DESCRIPTION} />
      ) : (
        <ul>
          {jobs.map((job) => (
            <JobRow key={job.job_name} job={job} windowHours={data?.window_hours ?? windowHours} />
          ))}
        </ul>
      )}
    </section>
  )
}

function JobRow({ job, windowHours }: { job: ScheduledJob; windowHours: number }) {
  const status = jobStatusCopy(job)
  const failing = job.failures_in_window > 0 || job.runs_in_window === 0

  return (
    <li data-slot="fleet-job" className="border-b border-border px-4 py-3 sm:px-5">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1">
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-2">
            <span data-slot="fleet-job-name" className="truncate font-mono text-[12.5px] text-foreground">
              {job.job_name}
            </span>
            <span className="inline-flex shrink-0 items-center gap-1.5" title={status.detail}>
              <StatusDot tone={TONE[status.tone]} />
              <span
                data-slot="fleet-job-status"
                className={cn(
                  'font-mono text-[9.5px] uppercase tracking-[0.12em]',
                  status.tone === 'ok' ? 'text-ok' : status.tone === 'warn' ? 'text-warn' : 'text-danger',
                )}
              >
                {status.label}
              </span>
            </span>
          </p>
          <p data-slot="fleet-job-meta" className="mt-0.5 text-[12px] leading-snug text-muted-foreground">
            {/*
              The LAST RUN is reported whatever the window, which is the whole
              point: a sweep that stopped four days ago must appear with that
              old timestamp rather than dropping off the list.
            */}
            last ran {relativeTime(job.last_started_at)} · took {formatDuration(job.last_duration_ms)}
          </p>
        </div>

        <p data-slot="fleet-job-runs" className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
          {/*
            Runs and failures always together. "12 failures" means something
            entirely different out of 12 runs than out of 2,880.
          */}
          {formatCount(job.failures_in_window)} failed of {formatCount(job.runs_in_window)} runs
          <span className="text-faint"> · last {windowHours}h</span>
        </p>
      </div>

      {failing && (
        <p data-slot="fleet-job-failure" className="mt-1.5 max-w-prose text-[12px] leading-snug text-faint">
          {job.last_failure_at !== null && <>Last failure {relativeTime(job.last_failure_at)}. </>}
          {JOB_ERROR_TEXT_WITHHELD}
        </p>
      )}
    </li>
  )
}

function JobSkeleton() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="border-b border-border px-4 py-3 sm:px-5">
          <div className="space-y-2">
            <Skeleton className="h-3.5 w-44" />
            <Skeleton className="h-2.5 w-60" />
          </div>
        </li>
      ))}
    </ul>
  )
}
