import { expect, test } from 'vitest'
import { isRedirect } from '@tanstack/react-router'
import { Route } from '../app.contacts.index'

// `?contact=<id>` selected a contact on the list and expanded an activity strip.
// That person has a record page now, but agent conversations already stored in the
// database link to the old param, so it redirects rather than silently doing
// nothing on a link a user can still scroll back to.

function runBeforeLoad(search: Record<string, unknown>): unknown {
  const beforeLoad = Route.options.beforeLoad
  if (typeof beforeLoad !== 'function') throw new Error('the route has no beforeLoad')
  try {
    // The real argument is the router's full match context; only `search` is read.
    beforeLoad({ search } as never)
  } catch (error) {
    return error
  }
  return undefined
}

test('?contact=<id> redirects to that contact\'s record page', () => {
  const thrown = runBeforeLoad({ contact: 'c-1' })

  expect(isRedirect(thrown)).toBe(true)
  // A redirect is a Response carrying the navigation under `options`.
  expect(thrown).toMatchObject({
    options: { to: '/app/contacts/$id', params: { id: 'c-1' }, replace: true },
  })
})

test('the list itself is left alone when no contact is named', () => {
  expect(runBeforeLoad({})).toBeUndefined()
  // A search or a list scope is the list's own business, not a record link.
  expect(runBeforeLoad({ q: 'acme', list: 'l-1' })).toBeUndefined()
})
