import { cn } from '@/lib/utils'
import { actorOrigin, actorTitle, originMeta, type Actor } from './actor'

/**
 * Attribution for a deal, note, task or event as a small pill, styled like
 * `HealthBadge` — including the native `title`, this codebase's cheapest
 * explain-this-flag affordance. `source` is optional: events carry no source,
 * deals do.
 */
export function ActorBadge({
  actor,
  source,
  className,
}: {
  actor: Actor
  source?: string
  className?: string
}) {
  const origin = actorOrigin(actor, source)
  const { label, icon: Icon, tone } = originMeta[origin]
  return (
    <span
      data-slot="actor-badge"
      data-origin={origin}
      title={actorTitle(actor, source)}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5',
        'font-mono text-[10.5px] font-medium uppercase tracking-[0.1em]',
        tone,
        className,
      )}
    >
      <Icon className="size-3 shrink-0" aria-hidden="true" />
      {origin === 'agent' && actor.client_id ? `${label} / ${actor.client_id}` : label}
    </span>
  )
}
