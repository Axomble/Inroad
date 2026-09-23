import { Handle, Position } from '@xyflow/react'
import { cn } from '@/lib/utils'
import { outputOffset, type FlowHandleId } from './node-registry'

/**
 * The box every node kind renders inside: an optional entry handle on top and
 * one exit handle per output along the bottom, spread evenly in the order the
 * registry declares them. A single-exit node passes `SINGLE_OUTPUT`; a
 * condition passes `['yes', 'no']` plus `outputLabels` so each exit is named in
 * text, never told apart by position or colour alone.
 *
 * Handles are only connectable when the canvas is (`connectable`) — otherwise
 * they are drawn purely to show where edges attach. `hasInput` must match the
 * registry entry's, which is what tells React Flow where the handles are.
 *
 * `pointer-events-auto`: React Flow sets `pointer-events: none` on the wrapper
 * of a node that is neither selectable nor draggable, and it inherits — without
 * this, none of the buttons a node renders could be clicked.
 */
export function FlowNodeFrame({
  hasInput = true,
  outputs,
  outputLabels,
  connectable = false,
  className,
  children,
}: {
  hasInput?: boolean
  outputs: readonly FlowHandleId[]
  outputLabels?: Readonly<Record<string, string>>
  connectable?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={cn('pointer-events-auto relative h-full w-full', className)}>
      {hasInput && <Handle type="target" position={Position.Top} isConnectable={connectable} />}
      {children}
      {outputs.map((handle, index) => {
        const left = `${outputOffset(index, outputs.length) * 100}%`
        const label = handle === null ? undefined : outputLabels?.[handle]
        return (
          <div key={handle ?? 'default'}>
            {label && (
              <span
                className="pointer-events-none absolute bottom-1.5 -translate-x-1/2 font-mono text-[9.5px] uppercase tracking-[0.12em] text-faint"
                style={{ left }}
              >
                {label}
              </span>
            )}
            <Handle
              type="source"
              position={Position.Bottom}
              id={handle ?? undefined}
              isConnectable={connectable}
              style={{ left }}
            />
          </div>
        )
      })}
    </div>
  )
}
