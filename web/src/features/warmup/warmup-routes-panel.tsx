import { cn } from '@/lib/utils'
import { InlineLoading, MutedEmpty, QueryErrorBanner } from '@/components/shared/record-page'
import { useGetMailboxWarmupQuery } from './api'
import { warmupErrorMessage } from './error-copy'
import {
  ROUTES_GATES_NOTHING,
  ROUTES_INTRO,
  ROUTE_RATE_COLUMNS,
  routesReading,
  type RouteRate,
  type RouteReading,
} from './route-copy'

/**
 * Where this mailbox's warmup mail was actually delivered, one row per
 * destination provider.
 *
 * A real `<table>` because this is a matrix and nothing else conveys "this cell
 * belongs to that destination and that rate" to a screen reader: every row is
 * headed by its destination (`scope="row"`) and every column by its rate.
 *
 * The reading comes wholly from `route-copy`, tones included. The distinctions
 * this panel exists to preserve — a single-route pool that must not read as a
 * clean matrix, `unknown` that is not a provider, a null rate that is not a zero
 * — are copy decisions, and JSX is where they get quietly flattened.
 *
 * It reads the detail query the card has already issued for every enrolled
 * mailbox, so opening it is a cache hit rather than a request. Owning the query
 * rather than taking a prop is deliberate: a detail request that is still in
 * flight, or that failed, must not arrive here as `routes: undefined` and render
 * as "nothing observed" — a failed load and an empty pool are different facts.
 *
 * Default export so the card can `React.lazy` it: the copy tables below are only
 * shipped once an operator opens a route matrix.
 */
export default function WarmupRoutesPanel({ mailboxId }: { mailboxId: string }) {
  const detail = useGetMailboxWarmupQuery({ id: mailboxId })
  const reading = routesReading(detail.data?.routes)

  return (
    <div data-slot="warmup-routes" className="border-t border-border bg-surface/40 px-4 py-3 sm:px-5">
      <p className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Destination routes</p>

      {detail.isLoading && <InlineLoading label="Loading destination routes" />}

      {detail.isError && (
        <QueryErrorBanner
          className=""
          message={warmupErrorMessage(
            detail.error,
            "This mailbox's destination routes couldn't be loaded, so nothing here is known.",
          )}
          onRetry={() => void detail.refetch()}
          retrying={detail.isFetching}
        />
      )}

      {!detail.isLoading && !detail.isError && (
        <>
          <p className="mb-3 max-w-prose text-[11.5px] leading-snug text-muted-foreground">{ROUTES_INTRO}</p>

          {reading.kind === 'unobserved' ? (
            <MutedEmpty text={reading.message} />
          ) : (
            <>
              {/*
                Above the matrix, never under it: an operator who reads one green
                row first has already drawn the wrong conclusion by the time a
                footnote tells them the pool only reaches one provider.
              */}
              {reading.soleNote && (
                <p data-slot="route-sole-destination" className="mb-3 max-w-prose text-[11.5px] leading-snug text-warn">
                  {reading.soleNote}
                </p>
              )}

              <div className="overflow-x-auto">
                <table className="w-full min-w-[34rem] border-collapse text-left">
                  <caption className="sr-only">
                    Placement by destination provider, each rate measured over that route's own observations
                  </caption>
                  <thead>
                    <tr className="border-b border-border">
                      <Heading>Destination</Heading>
                      {ROUTE_RATE_COLUMNS.map((column) => (
                        <Heading key={column}>{column}</Heading>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {reading.routes.map((route) => (
                      <RouteRow key={route.esp} route={route} />
                    ))}
                  </tbody>
                </table>
              </div>

              <p className="mt-3 text-[10.5px] leading-snug text-faint">{ROUTES_GATES_NOTHING}</p>
            </>
          )}
        </>
      )}
    </div>
  )
}

function Heading({ children }: { children: React.ReactNode }) {
  return (
    <th
      scope="col"
      className="py-1.5 pr-3 align-bottom font-mono text-[10px] font-normal uppercase tracking-[0.1em] text-faint last:pr-0"
    >
      {children}
    </th>
  )
}

function RouteRow({ route }: { route: RouteReading }) {
  return (
    <tr className="align-top">
      <th scope="row" className="min-w-0 py-2 pr-3 font-normal">
        <span className="flex items-start gap-1.5">
          {/*
            Shape, not colour, carries the distinction the row depends on: a
            filled node is a destination we resolved, a hollow one is a lookup
            that has not happened. The label says it too — this is redundancy for
            a scanning eye, never the only signal.
          */}
          <span
            aria-hidden="true"
            className={cn(
              'mt-1.5 size-1.5 shrink-0 rounded-full',
              route.resolved ? 'bg-current text-muted-foreground' : 'border border-current bg-transparent text-warn',
            )}
          />
          <span className="min-w-0">
            <span
              data-slot="route-destination"
              className={cn('block text-[12px] leading-snug', route.resolved ? 'text-foreground' : 'text-warn')}
            >
              {route.destination}
            </span>
            <Detail text={route.destinationDetail} />
          </span>
        </span>
      </th>
      {route.rates.map((rate) => (
        <RateCell key={rate.label} rate={rate} />
      ))}
    </tr>
  )
}

/**
 * One rate, and the population it was computed over — which is always this
 * route's own count and never the mailbox's pooled total. The population is not
 * optional chrome: two routes' percentages mean nothing side by side until both
 * denominators are visible, and a four-route split makes them very unequal.
 */
function RateCell({ rate }: { rate: RouteRate }) {
  return (
    <td className="py-2 pr-3 last:pr-0">
      <span
        data-slot="route-rate"
        className={cn('block text-[12px] leading-snug tabular-nums', rate.measured ? 'text-foreground' : 'text-muted-foreground')}
      >
        {rate.value}
      </span>
      <span data-slot="route-population" className="mt-0.5 block text-[10.5px] leading-snug tabular-nums text-faint">
        {rate.population}
      </span>
      {rate.detail && <Detail text={rate.detail} />}
    </td>
  )
}

function Detail({ text }: { text: string }) {
  return <span className="mt-0.5 block max-w-[22rem] text-[10.5px] leading-snug text-faint">{text}</span>
}
