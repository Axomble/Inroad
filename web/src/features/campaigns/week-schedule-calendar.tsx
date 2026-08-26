import { useEffect, useRef, useState } from 'react'
import { Copy, Plus, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { MINUTES_PER_DAY } from './schedule-time'
import {
  type Block,
  type BlockWeek,
  AXIS_HOURS,
  DISPLAY_WEEKDAYS,
  DISPLAY_WEEKDAYS_LONG,
  SNAP_MINUTES,
  clampMinute,
  copyDayToAll,
  defaultBlock,
  drawBlock,
  formatBlock,
  formatMinute,
  growToMinimum,
  mergeBlocks,
  moveBlock,
  openMinutes,
  resizeEnd,
  resizeStart,
  snapMinute,
} from './schedule-blocks'

/** Column height in px. Fixed so a minute maps to a stable pixel offset. */
const BODY_HEIGHT = 420

/** How tall an edge-grab zone is. Big enough to hit, small enough not to eat the body. */
const EDGE_PX = 7

type DragMode = 'move' | 'start' | 'end' | 'draw'

interface DragState {
  day: number
  index: number
  mode: DragMode
  /** Pointer Y where the gesture began, for move/resize deltas. */
  startY: number
  /** For a draw: the fixed edge the other end is dragged away from. */
  anchorMinute: number
  original: Block
  /** The column's viewport rect at gesture start, so Y maps to minutes. */
  rectTop: number
  rectHeight: number
}

/**
 * Calendar-style weekly editor for a campaign's send windows.
 *
 * Each Mon–Sun column owns an independent list of windows:
 *   • drag on empty column space to draw a new window
 *   • drag a window's body to move it, or its top/bottom edge to resize
 *   • × removes a window; the copy icon in a day header applies that day to all
 *
 * Everything snaps to 30 minutes, which is finer than the schema's hour-grid
 * alternative and — crucially — LOSSLESS against the API: windows are stored as
 * minute intervals, so nothing has to be rounded away on load or save.
 *
 * Blocks rather than toggled cells because a sending window is one object with
 * two edges, and that is what an operator adjusts: "start an hour later" is one
 * drag here, versus erasing and repainting a run of cells.
 */
export function WeekScheduleCalendar({
  week,
  onChange,
  disabled = false,
}: {
  week: BlockWeek
  onChange: (next: BlockWeek) => void
  disabled?: boolean
}) {
  const [drag, setDrag] = useState<DragState | null>(null)

  // Refs so the window-level move/up listeners read the freshest state without
  // re-subscribing on every pointer move (which would drop the gesture).
  const weekRef = useRef(week)
  weekRef.current = week
  const changeRef = useRef(onChange)
  changeRef.current = onChange

  const setDayBlocks = (day: number, blocks: Block[]) => {
    changeRef.current(weekRef.current.map((d, i) => (i === day ? blocks : d)))
  }
  const setBlock = (day: number, index: number, block: Block) => {
    setDayBlocks(day, (weekRef.current[day] ?? []).map((b, i) => (i === index ? block : b)))
  }

  useEffect(() => {
    if (!drag) return

    const yToMinute = (clientY: number) =>
      clampMinute(((clientY - drag.rectTop) / drag.rectHeight) * MINUTES_PER_DAY)

    const onMove = (event: PointerEvent) => {
      if (drag.mode === 'draw') {
        setBlock(drag.day, drag.index, drawBlock(drag.anchorMinute, yToMinute(event.clientY)))
        return
      }
      const deltaMinutes = ((event.clientY - drag.startY) / drag.rectHeight) * MINUTES_PER_DAY
      if (drag.mode === 'move') setBlock(drag.day, drag.index, moveBlock(drag.original, deltaMinutes))
      else if (drag.mode === 'start') setBlock(drag.day, drag.index, resizeStart(drag.original, deltaMinutes))
      else setBlock(drag.day, drag.index, resizeEnd(drag.original, deltaMinutes))
    }

    const onUp = () => {
      let blocks = weekRef.current[drag.day] ?? []
      if (drag.mode === 'draw') {
        // A flick is almost always a mis-click; grow it rather than discard the
        // gesture. Merging happens on release, not during — a block briefly
        // overlapping a neighbour mid-drag must not eat it irreversibly.
        blocks = blocks.map((b, i) => (i === drag.index ? growToMinimum(b) : b))
      }
      setDayBlocks(drag.day, mergeBlocks(blocks))
      setDrag(null)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    // Cursor and selection are set on the BODY: mid-drag the pointer leaves the
    // block it grabbed, so a cursor scoped to the element would flicker.
    const previousCursor = document.body.style.cursor
    document.body.style.cursor = drag.mode === 'move' ? 'grabbing' : 'ns-resize'
    document.body.style.userSelect = 'none'
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      document.body.style.cursor = previousCursor
      document.body.style.userSelect = ''
    }
    // `drag` is the whole dependency: the listeners close over the gesture that
    // started, and setBlock/setDayBlocks read live state through refs.
    // oxlint-disable-next-line exhaustive-deps -- see above; re-subscribing per move would drop the gesture
  }, [drag])

  const beginBlockDrag = (event: React.PointerEvent, day: number, index: number, mode: DragMode) => {
    if (disabled) return
    event.preventDefault()
    // Stops the column's own draw handler from also firing.
    event.stopPropagation()
    const block = week[day]?.[index]
    const rect = event.currentTarget.closest('[data-day-column]')?.getBoundingClientRect()
    if (!block || !rect) return
    setDrag({
      day,
      index,
      mode,
      startY: event.clientY,
      anchorMinute: block.start,
      original: block,
      rectTop: rect.top,
      rectHeight: rect.height,
    })
  }

  const beginDraw = (event: React.PointerEvent, day: number) => {
    if (disabled) return
    // Touch and pen deliberately do NOT draw: on a phone a vertical drag is how
    // you scroll the page, and stealing it would trap the user. They add via the
    // "+" in the day header instead, then drag the block to adjust.
    if (event.pointerType !== 'mouse') return
    const rect = event.currentTarget.getBoundingClientRect()
    const anchor = snapMinute(clampMinute(((event.clientY - rect.top) / rect.height) * MINUTES_PER_DAY))
    const blocks = week[day] ?? []
    const index = blocks.length
    setDayBlocks(day, [...blocks, drawBlock(anchor, anchor)])
    setDrag({
      day,
      index,
      mode: 'draw',
      startY: event.clientY,
      anchorMinute: anchor,
      original: { start: anchor, end: anchor + SNAP_MINUTES },
      rectTop: rect.top,
      rectHeight: rect.height,
    })
  }

  const addBlock = (day: number) => {
    if (disabled) return
    setDayBlocks(day, mergeBlocks([...(week[day] ?? []), defaultBlock()]))
  }
  const removeBlock = (day: number, index: number) => {
    if (disabled) return
    setDayBlocks(day, (week[day] ?? []).filter((_, i) => i !== index))
  }

  const totalMinutes = openMinutes(week)
  const totalHours = Math.round((totalMinutes / 60) * 10) / 10

  return (
    <div className={cn('space-y-2', disabled && 'pointer-events-none opacity-60')}>
      <div className="flex gap-1">
        {/* The time axis. aria-hidden: every block already announces its own
            day and range, so reading nine bare hour labels first is noise. */}
        <div className="relative w-10 shrink-0" style={{ height: BODY_HEIGHT }} aria-hidden="true">
          {AXIS_HOURS.map((hour) => (
            <span
              key={hour}
              className="absolute right-1 -translate-y-1/2 font-mono text-[9px] text-faint"
              style={{ top: `${(hour / 24) * 100}%` }}
            >
              {formatMinute(hour * 60)}
            </span>
          ))}
        </div>

        {DISPLAY_WEEKDAYS.map((label, day) => {
          const blocks = week[day] ?? []
          const longLabel = DISPLAY_WEEKDAYS_LONG[day] ?? label
          return (
            <div key={label} className="flex min-w-0 flex-1 flex-col">
              <div className="flex items-center justify-between gap-0.5 pb-1">
                <span className="font-mono text-[10px] text-muted-foreground">{label}</span>
                <span className="flex items-center gap-0.5">
                  <button
                    type="button"
                    aria-label={`Add a window on ${longLabel}`}
                    title="Add a window"
                    className="rounded p-0.5 text-faint hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
                    onClick={() => addBlock(day)}
                  >
                    <Plus className="size-3" />
                  </button>
                  <button
                    type="button"
                    aria-label={`Copy ${longLabel}'s windows to every day`}
                    title="Copy to every day"
                    className="rounded p-0.5 text-faint hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
                    onClick={() => !disabled && onChange(copyDayToAll(week, day))}
                  >
                    <Copy className="size-3" />
                  </button>
                </span>
              </div>

              <div
                data-day-column
                // A group, not a button: it contains the blocks, which are the
                // interactive things. Drawing is a mouse affordance layered on.
                role="group"
                aria-label={`${longLabel} sending windows`}
                className="relative min-w-0 flex-1 rounded-md border border-border bg-surface-2/40"
                style={{ height: BODY_HEIGHT }}
                onPointerDown={(e) => beginDraw(e, day)}
              >
                {/* Hour gridlines, purely to read position against. */}
                {AXIS_HOURS.slice(1, -1).map((hour) => (
                  <div
                    key={hour}
                    className="pointer-events-none absolute inset-x-0 border-t border-border/40"
                    style={{ top: `${(hour / 24) * 100}%` }}
                  />
                ))}

                {blocks.map((block, index) => {
                  const top = (block.start / MINUTES_PER_DAY) * 100
                  const height = ((block.end - block.start) / MINUTES_PER_DAY) * 100
                  return (
                    <div
                      // Keyed on the RANGE, not the index: blocks are re-sorted
                      // and merged on release, so a positional key would let
                      // React reuse a node for a different window. mergeBlocks
                      // guarantees a day has no two blocks with the same start.
                      key={`${block.start}-${block.end}`}
                      className="absolute inset-x-0.5 overflow-hidden rounded-[3px] bg-primary/85 text-primary-foreground"
                      style={{ top: `${top}%`, height: `${height}%` }}
                      onPointerDown={(e) => beginBlockDrag(e, day, index, 'move')}
                    >
                      {/* Edge grips. Rendered above the body so a grab near an
                          edge resizes rather than moves. */}
                      <div
                        className="absolute inset-x-0 top-0 cursor-ns-resize"
                        style={{ height: EDGE_PX }}
                        onPointerDown={(e) => beginBlockDrag(e, day, index, 'start')}
                      />
                      <div
                        className="absolute inset-x-0 bottom-0 cursor-ns-resize"
                        style={{ height: EDGE_PX }}
                        onPointerDown={(e) => beginBlockDrag(e, day, index, 'end')}
                      />
                      {/* The range is the accessible name AND the visible label,
                          so a keyboard user and a sighted one read the same thing. */}
                      <span className="pointer-events-none block truncate px-1 pt-0.5 font-mono text-[9px] leading-tight">
                        {formatBlock(block)}
                      </span>
                      <button
                        type="button"
                        aria-label={`Remove ${longLabel} ${formatBlock(block)}`}
                        className="absolute top-0 right-0 rounded p-0.5 opacity-70 hover:opacity-100 focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
                        onPointerDown={(e) => e.stopPropagation()}
                        onClick={() => removeBlock(day, index)}
                      >
                        <X className="size-2.5" />
                      </button>
                    </div>
                  )
                })}
              </div>
            </div>
          )
        })}
      </div>

      <p className={cn('font-mono text-[10px]', totalMinutes === 0 ? 'text-warn' : 'text-faint')}>
        {totalMinutes === 0
          ? 'Nothing is open — a campaign needs at least one sending window.'
          : `${totalHours} sending ${totalHours === 1 ? 'hour' : 'hours'} a week · drag to draw, drag an edge to resize`}
      </p>
    </div>
  )
}
