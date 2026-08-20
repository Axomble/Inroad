import { expect, test } from 'vitest'
import { isRedirect } from '@tanstack/react-router'
import { Route } from '../app.crm'

// `/app/crm` was the console the sidebar mislabelled "CRM". The path outlives the
// page because links, bookmarks and agent-emitted URLs already point at it, so
// the redirect is part of the contract, not a nicety.

test('/app/crm redirects to the honestly-named Companies route', () => {
  const beforeLoad = Route.options.beforeLoad
  if (typeof beforeLoad !== 'function') throw new Error('the route has no beforeLoad to redirect from')

  let thrown: unknown
  try {
    // The signature is the router's full match context; the redirect ignores it.
    beforeLoad({} as never)
  } catch (error) {
    thrown = error
  }

  expect(isRedirect(thrown)).toBe(true)
  // A redirect is a Response carrying the navigation under `options`.
  expect(thrown).toMatchObject({ options: { to: '/app/companies' } })
})

test('the redirect replaces the history entry so Back does not bounce', () => {
  const beforeLoad = Route.options.beforeLoad
  if (typeof beforeLoad !== 'function') throw new Error('the route has no beforeLoad to redirect from')

  let thrown: unknown
  try {
    beforeLoad({} as never)
  } catch (error) {
    thrown = error
  }

  expect(thrown).toMatchObject({ options: { replace: true } })
})
