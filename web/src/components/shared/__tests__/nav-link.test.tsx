import { expect, test } from 'vitest'
import { render, screen } from '@testing-library/react'
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import { NavLink } from '../nav-link'

/**
 * The regression this primitive exists to prevent, tested on a REAL router
 * rather than a mocked `Link` — the sidebar's own suite mocks the router
 * wholesale, which is exactly why a wrong `activeOptions` went unnoticed there
 * for two releases.
 *
 * Both links below point at the SAME path and differ only in `exact`, so the
 * flag is the only variable in the experiment.
 */
const rootRoute = createRootRoute({
  component: () => (
    <>
      <NavLink to="/parent" exact activeClassName="is-active" data-slot="exact-link">
        Exact
      </NavLink>
      <NavLink to="/parent" exact={false} activeClassName="is-active" data-slot="prefix-link">
        Prefix
      </NavLink>
      <Outlet />
    </>
  ),
})

const parentRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/parent',
  component: () => <div>parent screen</div>,
})

const childRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/parent/child',
  component: () => <div>child screen</div>,
})

const routeTree = rootRoute.addChildren([parentRoute, childRoute])

function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  // `as never`: the app augments TanStack's `Register` interface with its own
  // generated route tree, so `RouterProvider` is typed to that router. The
  // campaign-detail suite uses the same escape hatch to drive a test router.
  return render(<RouterProvider router={router as never} />)
}

const exactLink = () => document.querySelector('[data-slot="exact-link"]')
const prefixLink = () => document.querySelector('[data-slot="prefix-link"]')

test('on its own path, both matching modes light up', async () => {
  renderAt('/parent')

  expect(await screen.findByText('parent screen')).toBeInTheDocument()
  expect(exactLink()).toHaveAttribute('data-status', 'active')
  expect(prefixLink()).toHaveAttribute('data-status', 'active')
})

test('on a child path, only the prefix-matching row stays lit', async () => {
  renderAt('/parent/child')

  expect(await screen.findByText('child screen')).toBeInTheDocument()
  // The bug: a parent row that keeps its highlight on every child route, so two
  // rows read as selected at once.
  expect(exactLink()).not.toHaveAttribute('data-status', 'active')
  // Still correct for a section row — Campaigns stays lit on a campaign's tabs.
  expect(prefixLink()).toHaveAttribute('data-status', 'active')
})

test('the active row carries the caller-supplied active class, not a baked-in one', async () => {
  renderAt('/parent')

  await screen.findByText('parent screen')
  expect(exactLink()?.className).toContain('is-active')
})
