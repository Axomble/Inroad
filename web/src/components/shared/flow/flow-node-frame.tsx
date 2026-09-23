import { Handle, Position } from '@xyflow/react'
import { cn } from '@/lib/utils'
import { outputOffset } from './node-registry'
import { useFlowRegistry } from './flow-registry-context'

/**
 * The box every node kind renders inside: an entry handle on top (unless the
 * kind is an entry point) and one exit handle per registered output along the
 * bottom, each named in text when its output has a label. Both come from the
 * registry by `type` — pass the `type` React Flow gives the node component.
 *
 * Handles are only connectable when the canvas is (`connectable`) — otherwise
 * they are drawn purely to show where edges attach.
 *
 * `pointer-events-auto`: React Flow sets `pointer-events: none` on the wrapper
 * of a node that is neither selectable nor draggable, and it inherits — without
 * this, none of the buttons a node renders could be clicked.
 */
export function FlowNodeFrame({
  type,
  connectable = false,
  className,
  children,
}: {
  type: string
  connectable?: boolean
  className?: string
  children: React.ReactNode
}) {
  const registry = useFlowRegistry()
  const outputs = registry.outputsOf(type)
  return (
    <div className={cn('pointer-events-auto relative h-full w-full', className)}>
      {registry.hasInputOf(type) && <Handle type="target" position={Position.Top} isConnectable={connectable} />}
      {children}
      {outputs.map((output, index) => {
        const left = `${outputOffset(index, outputs.length) * 100}%`
        return (
          <div key={output.id ?? 'default'}>
            {output.label && (
              <span
                className="pointer-events-none absolute bottom-1.5 -translate-x-1/2 font-mono text-[9.5px] uppercase tracking-[0.12em] text-faint"
                style={{ left }}
              >
                {output.label}
              </span>
            )}
            <Handle
              type="source"
              position={Position.Bottom}
              id={output.id ?? undefined}
              isConnectable={connectable}
              style={{ left }}
            />
          </div>
        )
      })}
    </div>
  )
}
