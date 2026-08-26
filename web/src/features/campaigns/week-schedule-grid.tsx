import { useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { WEEKDAY_SHORT, WEEKDAY_LABELS } from './schedule-time'
import {
  type CellGrid,
  HOURS_PER_DAY,
  DAYS_PER_WEEK,
  paintRange,
  setDay,
  setHour,
  hourLabel,
  openHourCount,
} from './schedule-cells'

/** Hour ticks worth labelling — every third, so the axis stays legible at width. */
const LABELLED_HOURS = [0, 3, 6, 9, 12, 15, 18, 21]

/**
 * A weekday × hour board for a campaign's send windows.
 *
 * Click a cell to toggle it; drag to paint a rectangle. Rectangular rather than
 * reading-order because "Mon–Fri, 9–5" is how a working week is described — a
 * reading-order sweep would also catch Monday evening and Friday morning.
 *
 * The drag's mode is decided by the cell it STARTS on: beginning on an open cell
 * erases, beginning on a shut one fills. That way one gesture never both adds
 * and removes, which is impossible to predict mid-drag.
 *
 * Keyboard: every cell is a real button, so tabbing and Enter/Space work without
 * any extra handling. The row and column headers double as "whole day" and
 * "this hour every day" toggles, which is also how a keyboard user selects in
 * bulk without dragging.
 */
export function WeekScheduleGrid({
  grid,
  onChange,
  disabled = false,
}: {
  grid: CellGrid
  onChange: (next: CellGrid) => void
  disabled?: boolean
}) {
  // The in-progress drag. null when not dragging; `open` is fixed at
  // mousedown so the whole gesture has one mode.
  const [drag, setDrag] = useState<{ from: { day: number; hour: number }; open: boolean } | null>(null)
  // The grid as it looked when the drag began, so each move repaints from the
  // original rather than accumulating over its own output.
  const baseRef = useRef<CellGrid>(grid)

  // A drag must end even if the pointer is released outside the board —
  // otherwise the next hover would keep painting with no button held.
  useEffect(() => {
    if (!drag) return
    const end = () => setDrag(null)
    window.addEventListener('mouseup', end)
    return () => window.removeEventListener('mouseup', end)
  }, [drag])

  const beginDrag = (day: number, hour: number) => {
    if (disabled) return
    const open = !(grid[day]?.[hour] === true)
    baseRef.current = grid
    setDrag({ from: { day, hour }, open })
    onChange(paintRange(grid, { day, hour }, { day, hour }, open))
  }

  const extendDrag = (day: number, hour: number) => {
    if (!drag || disabled) return
    onChange(paintRange(baseRef.current, drag.from, { day, hour }, drag.open))
  }

  const toggleCell = (day: number, hour: number) => {
    if (disabled) return
    onChange(paintRange(grid, { day, hour }, { day, hour }, !(grid[day]?.[hour] === true)))
  }

  const openHours = openHourCount(grid)

  return (
    <div className="space-y-2">
      {/* select-none: without it a drag across cells selects the label text
          instead of painting. */}
      <div className={cn('select-none', disabled && 'pointer-events-none opacity-60')}>
        {/* The hour axis. aria-hidden because each cell already names its own
            day and hour — a screen reader reading 24 bare numbers first would
            only add noise. */}
        <div className="flex items-end gap-px pl-12" aria-hidden="true">
          {Array.from({ length: HOURS_PER_DAY }, (_, hour) => (
            <button
              key={hour}
              type="button"
              tabIndex={-1}
              title={`Toggle ${hourLabel(hour)} on every day`}
              className="min-w-0 flex-1 pb-1 font-mono text-[9px] text-faint hover:text-foreground"
              onClick={() => {
                if (disabled) return
                // Fill unless the column is already fully open — so the first
                // click on a partly-open column completes it rather than
                // clearing what is there.
                const allOpen = grid.every((row) => row[hour] === true)
                onChange(setHour(grid, hour, !allOpen))
              }}
            >
              {LABELLED_HOURS.includes(hour) ? hourLabel(hour) : ''}
            </button>
          ))}
        </div>

        {Array.from({ length: DAYS_PER_WEEK }, (_day, day) => (
          <div key={day} className="flex items-center gap-px">
            <button
              type="button"
              className="w-12 shrink-0 py-0.5 pr-2 text-right font-mono text-[10px] text-muted-foreground hover:text-foreground"
              onClick={() => {
                if (disabled) return
                const allOpen = grid[day]?.every(Boolean) === true
                onChange(setDay(grid, day, !allOpen))
              }}
            >
              {WEEKDAY_SHORT[day]}
            </button>
            {Array.from({ length: HOURS_PER_DAY }, (_hour, hour) => {
              const open = grid[day]?.[hour] === true
              return (
                <button
                  key={hour}
                  type="button"
                  // The cell's own label carries both coordinates and its state,
                  // so a screen reader never depends on the visual axis.
                  aria-label={`${WEEKDAY_LABELS[day]} ${hourLabel(hour)}, ${open ? 'open' : 'closed'}`}
                  aria-pressed={open}
                  className={cn(
                    'h-5 min-w-0 flex-1 rounded-[2px] transition-colors',
                    'focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none',
                    open ? 'bg-primary hover:bg-primary/80' : 'bg-surface-2 hover:bg-surface-2/60',
                  )}
                  onMouseDown={(e) => {
                    // Left button only: a right-click should open the context
                    // menu, not begin painting.
                    if (e.button !== 0) return
                    e.preventDefault()
                    beginDrag(day, hour)
                  }}
                  onMouseEnter={() => extendDrag(day, hour)}
                  // Keyboard activation lands here too, where there is no drag
                  // in progress — so Enter/Space toggles the single cell.
                  onClick={() => {
                    if (drag) return
                    toggleCell(day, hour)
                  }}
                />
              )
            })}
          </div>
        ))}
      </div>

      {/* The board is a picture; this is the number. Also the one place a
          fully-closed week is stated plainly, since the API rejects it. */}
      <p className={cn('font-mono text-[10px]', openHours === 0 ? 'text-warn' : 'text-faint')}>
        {openHours === 0
          ? 'Nothing is open — a campaign needs at least one sending hour.'
          : `${openHours} sending ${openHours === 1 ? 'hour' : 'hours'} a week`}
      </p>
    </div>
  )
}
