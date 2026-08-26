import { useState } from 'react'
import { Clock, BellOff, AlarmClockOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { httpStatus } from '@/lib/rtk-error'
import { useSnoozeInboxThreadMutation, useUnsnoozeInboxThreadMutation, type InboxSnooze } from './api'
import {
  offerablePresets,
  parseCustomSnooze,
  toDateTimeLocalValue,
  formatSnoozeUntil,
  SNOOZE_MAX_DAYS,
} from './snooze-presets'

/**
 * Snooze / unsnooze control for one thread.
 *
 * When the thread is already snoozed this becomes an "Snoozed until X" button
 * whose menu offers Unsnooze plus the same presets (re-snoozing to a new
 * moment is one click, matching the API's replace-not-conflict semantics).
 *
 * The presets are resolved once per open, not per render, so every item in one
 * menu is measured from the same instant — see offerablePresets.
 */
export function SnoozeMenu({ threadId, snooze }: { threadId: string; snooze: InboxSnooze | null | undefined }) {
  const [open, setOpen] = useState(false)
  const [now, setNow] = useState(() => new Date())
  const [custom, setCustom] = useState('')
  const [customError, setCustomError] = useState<string | null>(null)

  const [snoozeThread, { isLoading: isSnoozing, error: snoozeError }] = useSnoozeInboxThreadMutation()
  const [unsnoozeThread, { isLoading: isUnsnoozing, error: unsnoozeError }] = useUnsnoozeInboxThreadMutation()
  const busy = isSnoozing || isUnsnoozing
  const error = snoozeError ?? unsnoozeError

  // Re-read the clock each time the menu opens: a tab left open for hours
  // would otherwise offer "Later today" measured from this morning.
  const onOpenChange = (next: boolean) => {
    if (next) {
      setNow(new Date())
      setCustom('')
      setCustomError(null)
    }
    setOpen(next)
  }

  const apply = async (at: Date) => {
    // toISOString() is exactly the RFC3339 the API wants, and UTC is the right
    // wire form — the instant is absolute, only its *display* is local.
    await snoozeThread({ id: threadId, snoozeInboxThreadRequest: { snooze_until: at.toISOString() } })
    setOpen(false)
  }

  const applyCustom = () => {
    const result = parseCustomSnooze(custom, new Date())
    if (!result.ok) {
      setCustomError(result.reason)
      return
    }
    setCustomError(null)
    void apply(result.at)
  }

  const presets = offerablePresets(now)
  const snoozedUntil = snooze ? new Date(snooze.snooze_until) : null

  return (
    <div className="flex flex-col items-end gap-1">
      <DropdownMenu open={open} onOpenChange={onOpenChange}>
        <DropdownMenuTrigger asChild>
          <Button
            variant={snoozedUntil ? 'secondary' : 'ghost'}
            size="xs"
            disabled={busy}
            aria-label={snoozedUntil ? `Snoozed until ${snoozedUntil.toLocaleString()}. Change or remove` : 'Snooze this thread'}
          >
            {snoozedUntil ? <BellOff className="size-3.5" /> : <Clock className="size-3.5" />}
            {snoozedUntil ? formatSnoozeUntil(snoozedUntil, now) : 'Snooze'}
          </Button>
        </DropdownMenuTrigger>

        <DropdownMenuContent align="end" className="w-60">
          {snoozedUntil && (
            <>
              <DropdownMenuItem onSelect={() => void unsnoozeThread({ id: threadId })}>
                <AlarmClockOff className="size-3.5" />
                Unsnooze now
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>Snooze again until</DropdownMenuLabel>
            </>
          )}
          {!snoozedUntil && <DropdownMenuLabel>Snooze until</DropdownMenuLabel>}

          {presets.map((preset) => (
            <DropdownMenuItem key={preset.id} onSelect={() => void apply(preset.at)}>
              <span className="flex-1">{preset.label}</span>
              <span className="font-mono text-[10px] text-faint">{formatSnoozeUntil(preset.at, now)}</span>
            </DropdownMenuItem>
          ))}

          <DropdownMenuSeparator />
          <DropdownMenuLabel>Pick a time</DropdownMenuLabel>
          {/* onSelect is prevented so typing in the field doesn't close the
              menu — a Radix item swallows interaction as a selection otherwise. */}
          <div
            className="px-2 pb-2"
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                applyCustom()
              }
            }}
          >
            <Input
              type="datetime-local"
              value={custom}
              min={toDateTimeLocalValue(now)}
              onChange={(e) => {
                setCustom(e.target.value)
                setCustomError(null)
              }}
              aria-label={`Snooze until a specific date and time, up to ${SNOOZE_MAX_DAYS} days ahead`}
              aria-invalid={customError !== null}
              className="h-8 text-[12px]"
            />
            {customError && (
              <p role="alert" className="mt-1 text-[11px] text-danger">
                {customError}
              </p>
            )}
            <Button size="xs" variant="secondary" className="mt-2 w-full" disabled={busy} onClick={applyCustom}>
              Snooze
            </Button>
          </div>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* A failed snooze must be visible: the operator believes the thread is
          parked, and silence here would lose it. 422 is the server's own bound
          rejecting the moment; anything else is reported by status. */}
      {error !== undefined && (
        <p role="alert" className="text-[11px] text-danger">
          {httpStatus(error) === 422
            ? `Pick a moment in the future, within ${SNOOZE_MAX_DAYS} days.`
            : `Couldn't update the snooze${httpStatus(error) ? ` (${httpStatus(error)})` : ''}.`}
        </p>
      )}
    </div>
  )
}
