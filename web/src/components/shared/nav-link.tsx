import { Link } from '@tanstack/react-router'

/**
 * The one place router active-state is configured.
 *
 * TanStack matches a `Link`'s `to` against route *prefixes*, which is right for
 * a section row (Campaigns stays lit on `/app/campaigns/$id/steps`) and wrong
 * for any row whose path is the parent of its siblings — that row then stays
 * lit everywhere and two things look selected at once.
 *
 * That bug has now been fixed twice, in two different navs, because each one
 * called `Link` directly and had to remember `activeOptions` on its own: once
 * in the campaign tabs, where Overview (`/app/campaigns/$id`) is the parent of
 * the other four, and once in the sidebar, where `/app` is the parent of every
 * screen in the product. A third nav would have inherited it too.
 *
 * So `exact` is REQUIRED here, not defaulted. Every row has to answer "do I own
 * child routes?" out loud, and forgetting is a type error instead of a
 * highlight that is quietly wrong on every page. That is the whole point of
 * this component — it owns no styling, only that decision.
 */
export interface NavLinkProps {
  to: string
  /** Path params for a parameterized route, e.g. `{ id }` for `/app/campaigns/$id`. */
  params?: Record<string, string>
  /**
   * `true` when this row should light up ONLY on its own path — use it for any
   * row that is the parent of its siblings. `false` keeps prefix matching, so
   * the row stays lit on its child routes.
   */
  exact: boolean
  /** Classes in the resting state. */
  className?: string
  /** Classes added by the router when this row is the active one. */
  activeClassName: string
  /** Marks the rendered element for styling/testing hooks, per the house convention. */
  'data-slot'?: string
  children: React.ReactNode
}

export function NavLink({ to, params, exact, className, activeClassName, children, ...props }: NavLinkProps) {
  return (
    <Link
      to={to}
      params={params}
      className={className}
      activeOptions={{ exact }}
      activeProps={{ className: activeClassName }}
      {...props}
    >
      {children}
    </Link>
  )
}
