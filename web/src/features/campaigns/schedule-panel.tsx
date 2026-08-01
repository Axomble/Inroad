import { useEffect, useMemo, useState } from 'react'
import { Clock, Plus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionBar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { cn } from '@/lib/utils'
import { useGetCampaignScheduleQuery, useUpdateCampaignScheduleMutation } from './api'
import type { CampaignSchedule } from './api'
import { WEEKDAY_SHORT } from './schedule-time'
import {
  EMPTY_WEEK,
  dailyLimitFromDraft,
  dailyLimitToDraft,
  MAX_DAILY_LIMIT,
  fromDraft,
  newInterval,
  scheduleErrorMessage,
  toDraft,
} from './schedule-draft'
import type { DraftWeek } from './schedule-draft'

/**
 * The campaign's sending plan: when (timezone plus a window per weekday) and how
 * much (the campaign-wide daily limit).
 *
 * Sends are placed inside these windows, spread through a distribution curve and
 * nudged off the clock grid, so the preview below the editor shows real upcoming
 * instants — the point being that the operator can see the cadence rather than
 * take it on trust.
 */
export function SchedulePanel({ campaignId }: { campaignId: string }) {
  const { data, isLoading, error } = useGetCampaignScheduleQuery({ id: campaignId })
  const [save, { isLoading: isSaving, error: saveError, isSuccess }] = useUpdateCampaignScheduleMutation()

  const [timezone, setTimezone] = useState('')
  const [week, setWeek] = useState<DraftWeek>(EMPTY_WEEK)
  const [dailyLimit, setDailyLimit] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  // Seed the editor from the server once it arrives, and re-seed after a save so
  // the form reflects what was actually persisted. Guarded on `dirty` so a
  // background refetch can't discard edits in progress.
  useEffect(() => {
    if (!data || dirty) return
    setTimezone(data.timezone)
    setWeek(toDraft(data.days))
    setDailyLimit(dailyLimitToDraft(data.daily_limit))
  }, [data, dirty])

  const zones = useMemo(supportedTimezones, [])

  function edit(next: DraftWeek) {
    setWeek(next)
    setDirty(true)
    setProblem(null)
  }

  function addInterval(weekday: number) {
    const next = week.map((intervals, i) =>
      i === weekday ? [...intervals, newInterval()] : intervals,
    )
    edit(next)
  }

  function removeInterval(weekday: number, index: number) {
    const next = week.map((intervals, i) =>
      i === weekday ? intervals.filter((_, j) => j !== index) : intervals,
    )
    edit(next)
  }

  function setBound(weekday: number, index: number, bound: 'start' | 'end', value: string) {
    const next = week.map((intervals, i) =>
      i === weekday
        ? intervals.map((iv, j) => (j === index ? { ...iv, [bound]: value } : iv))
        : intervals,
    )
    edit(next)
  }

  async function onSave() {
    const result = fromDraft(week)
    if ('problem' in result) {
      setProblem(result.problem)
      return
    }
    const limit = dailyLimitFromDraft(dailyLimit)
    if ('problem' in limit) {
      setProblem(limit.problem)
      return
    }
    try {
      await save({
        id: campaignId,
        campaignScheduleRequest: {
          timezone: timezone || 'UTC',
          days: result.days,
          // Explicitly null rather than omitted: this PUT is a full replace, so
          // an omitted field would leave a previously set limit in place and the
          // cleared field would silently not take effect.
          daily_limit: limit.dailyLimit,
        },
      }).unwrap()
      setDirty(false)
      setProblem(null)
    } catch {
      // The rejected promise is surfaced through `saveError` below; swallowing it
      // here only stops the unhandled rejection, it isn't the error handling.
    }
  }

  if (isLoading) {
    return (
      <div className="border-b border-border">
        <SectionBar label="Sending schedule" />
        <div className="space-y-2 px-5 py-4">
          <Skeleton className="h-5 w-48" />
          <Skeleton className="h-5 w-64" />
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="border-b border-border">
        <SectionBar label="Sending schedule" />
        <div role="alert" className="px-5 py-6 text-sm text-danger">
          Couldn't load the schedule{httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.
        </div>
      </div>
    )
  }

  return (
    <div className="border-b border-border">
      <SectionBar label="Sending schedule">
        {dirty && (
          <Button size="xs" onClick={onSave} disabled={isSaving}>
            {isSaving ? 'Saving…' : 'Save schedule'}
          </Button>
        )}
      </SectionBar>

      <div className="space-y-4 px-5 py-4">
        <div className="max-w-xs space-y-1.5">
          <Label htmlFor="schedule-timezone">Timezone</Label>
          <Select
            id="schedule-timezone"
            value={timezone}
            onChange={(e) => {
              setTimezone(e.target.value)
              setDirty(true)
              setProblem(null)
            }}
          >
            {/* The current value is always present, even if the browser doesn't
                enumerate that zone, so a server-set zone is never silently lost. */}
            {!zones.includes(timezone) && timezone !== '' && <option value={timezone}>{timezone}</option>}
            {zones.map((zone) => (
              <option key={zone} value={zone}>
                {zone}
              </option>
            ))}
          </Select>
          <p className="text-xs text-muted-foreground">Windows below are in this timezone.</p>
        </div>

        <ul className="divide-y divide-border rounded-md border border-border">
          {WEEKDAY_SHORT.map((label, weekday) => {
            const intervals = week[weekday] ?? []
            return (
              <li key={label} className="flex flex-wrap items-center gap-2 px-3 py-2">
                <span
                  className={cn(
                    'w-10 shrink-0 text-xs font-medium',
                    intervals.length === 0 ? 'text-faint' : 'text-foreground',
                  )}
                >
                  {label}
                </span>
                {intervals.length === 0 ? (
                  <span className="text-xs text-faint">No sending</span>
                ) : (
                  <div className="flex flex-wrap items-center gap-2">
                    {intervals.map((iv, index) => (
                      <span key={iv.id} className="flex items-center gap-1">
                        <Input
                          type="time"
                          aria-label={`${label} window ${index + 1} start`}
                          className="h-8 w-[7.5rem]"
                          value={iv.start}
                          onChange={(e) => setBound(weekday, index, 'start', e.target.value)}
                        />
                        <span className="text-xs text-muted-foreground">to</span>
                        <Input
                          type="time"
                          aria-label={`${label} window ${index + 1} end`}
                          className="h-8 w-[7.5rem]"
                          value={iv.end}
                          onChange={(e) => setBound(weekday, index, 'end', e.target.value)}
                        />
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove ${label} window ${index + 1}`}
                          onClick={() => removeInterval(weekday, index)}
                        >
                          <X className="size-3.5" />
                        </Button>
                      </span>
                    ))}
                  </div>
                )}
                <Button
                  variant="ghost"
                  size="xs"
                  className="ml-auto"
                  aria-label={`Add a ${label} window`}
                  onClick={() => addInterval(weekday)}
                >
                  <Plus className="size-3.5" />
                  Add
                </Button>
              </li>
            )
          })}
        </ul>

        <div className="max-w-xs space-y-1.5">
          <Label htmlFor="schedule-daily-limit">Daily limit</Label>
          <Input
            id="schedule-daily-limit"
            type="number"
            min={1}
            max={MAX_DAILY_LIMIT}
            inputMode="numeric"
            placeholder="No limit"
            value={dailyLimit}
            onChange={(e) => {
              setDailyLimit(e.target.value)
              setDirty(true)
              setProblem(null)
            }}
          />
          {/* Spelled out because "daily limit" reads as per-mailbox: an operator
              who assumes that under-configures the campaign by the size of the
              pool. The per-mailbox ceiling is the mailbox's own daily cap. */}
          <p className="text-xs text-muted-foreground">
            The most this campaign sends per day in total, added up across{' '}
            <strong className="font-medium">every sender in its pool</strong> — not per mailbox. Leave it
            empty for no campaign limit. Each mailbox still keeps its own daily cap, so this can only lower
            volume, never raise it.
          </p>
        </div>

        {problem && (
          <p role="alert" className="text-sm text-danger">
            {problem}
          </p>
        )}
        {saveError && (
          <p role="alert" className="text-sm text-danger">
            {scheduleErrorMessage(saveError)}
          </p>
        )}
        {isSuccess && !dirty && !saveError && (
          <p className="text-sm text-muted-foreground">Schedule saved. It applies to sends scheduled from now on.</p>
        )}

        <SchedulePreview preview={data?.preview} dirty={dirty} />
      </div>
    </div>
  )
}

/**
 * The next few instants this schedule produces. Hidden while the draft is dirty:
 * the preview comes from the saved schedule, so showing it next to unsaved edits
 * would claim a cadence that isn't in effect.
 */
function SchedulePreview({ preview, dirty }: { preview?: CampaignSchedule['preview']; dirty: boolean }) {
  if (dirty) {
    return <p className="text-xs text-muted-foreground">Save to see the send times this schedule produces.</p>
  }
  if (!preview || preview.length === 0) return null
  return (
    <div className="space-y-1">
      <p className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <Clock className="size-3.5" />
        Next send times
      </p>
      <p className="font-mono text-xs text-foreground">{preview.join(' · ')}</p>
      <p className="text-xs text-muted-foreground">
        Spread across the window and nudged off exact minutes, so sends don't arrive on a fixed interval.
      </p>
    </div>
  )
}

/**
 * The browser's IANA zone list when available, falling back to a short common
 * set. `supportedValuesOf` is missing in older Safari, so the fallback keeps the
 * control usable rather than rendering an empty select.
 */
function supportedTimezones(): string[] {
  const intl = Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }
  const zones = intl.supportedValuesOf?.('timeZone')
  if (zones && zones.length > 0) return zones
  return [
    'UTC',
    'America/Los_Angeles',
    'America/Chicago',
    'America/New_York',
    'Europe/London',
    'Europe/Berlin',
    'Asia/Karachi',
    'Asia/Kolkata',
    'Asia/Singapore',
    'Australia/Sydney',
  ]
}

