import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ContactsPage } from '../contacts-page'

// Driven through ContactsPage rather than the actions component alone: the
// ?list= URL selection clearing on delete is page behaviour, and the sidebar
// refetching after a rename is cache-tag behaviour — neither is visible from
// the dialog in isolation.

const router = vi.hoisted(() => {
  const listeners = new Set<() => void>()
  const state = {
    search: {} as Record<string, unknown>,
    listeners,
    subscribe: (cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    navigate: (options: { search: (prev: Record<string, unknown>) => Record<string, unknown> }) => {
      state.search = options.search(state.search)
      for (const cb of listeners) cb()
      return Promise.resolve()
    },
  }
  return state
})

vi.mock('@tanstack/react-router', async () => {
  const { useSyncExternalStore } = await import('react')
  return {
    useSearch: () => useSyncExternalStore(router.subscribe, () => router.search),
    useNavigate: () => router.navigate,
  }
})

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

let lists: { id: string; name: string }[]
let renameResponder: (body: { name: string }) => Response
let deleteResponder: () => Response
let renameBodies: { name: string }[]

beforeEach(() => {
  router.search = {}
  lists = [{ id: 'list-1', name: 'SaaS founders' }]
  renameBodies = []
  renameResponder = (body) => {
    lists = lists.map((l) => (l.id === 'list-1' ? { ...l, name: body.name } : l))
    return json({ id: 'list-1', name: body.name })
  }
  deleteResponder = () => {
    lists = lists.filter((l) => l.id !== 'list-1')
    return new Response(null, { status: 204 })
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()

      if (url.pathname.endsWith('/lists') && method === 'GET') return json(lists)
      if (url.pathname.endsWith('/lists/list-1') && method === 'PATCH') {
        const raw = isRequest ? await input.text() : String(init?.body)
        const body = JSON.parse(raw) as { name: string }
        renameBodies.push(body)
        return renameResponder(body)
      }
      if (url.pathname.endsWith('/lists/list-1') && method === 'DELETE') return deleteResponder()
      if (url.pathname.endsWith('/contacts') && method === 'GET') {
        return json({ items: [], next_cursor: null, prev_cursor: null, total: 0, total_is_capped: false })
      }
      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renaming a list sends the trimmed name and the sidebar refetches to show it', async () => {
  renderWithProviders(<ContactsPage />)
  fireEvent.click(await screen.findByRole('button', { name: /rename list saas founders/i }))

  const input = await screen.findByLabelText('Name')
  expect(input).toHaveValue('SaaS founders')
  fireEvent.change(input, { target: { value: '  Founders EU  ' } })
  fireEvent.click(screen.getByRole('button', { name: /^rename list$/i }))

  // The invalidated List tag refetches the sidebar, which now shows the new name.
  expect(await screen.findByText('Founders EU')).toBeInTheDocument()
  expect(renameBodies).toEqual([{ name: 'Founders EU' }])
  // Dialog closed on success.
  await waitFor(() => expect(screen.queryByLabelText('Name')).not.toBeInTheDocument())
})

test('an empty rename is blocked client-side and nothing is sent', async () => {
  renderWithProviders(<ContactsPage />)
  fireEvent.click(await screen.findByRole('button', { name: /rename list saas founders/i }))

  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: '   ' } })
  fireEvent.click(screen.getByRole('button', { name: /^rename list$/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/name the list/i)
  expect(renameBodies).toEqual([])
})

test('a delete blocked by a campaign (409) is explained distinctly and the dialog stays open', async () => {
  deleteResponder = () => json({ error: 'list is used by a campaign' }, 409)

  renderWithProviders(<ContactsPage />)
  fireEvent.click(await screen.findByRole('button', { name: /delete list saas founders/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^delete list$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/used by a campaign/i)
  // Still open so the user can read it and cancel; the row is untouched.
  expect(screen.getByRole('button', { name: /cancel/i })).toBeInTheDocument()
  expect(screen.getByText('SaaS founders')).toBeInTheDocument()
})

test('deleting the currently selected list falls back to the all-contacts scope', async () => {
  router.search = { list: 'list-1' }

  renderWithProviders(<ContactsPage />)
  fireEvent.click(await screen.findByRole('button', { name: /delete list saas founders/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^delete list$/i }))

  await waitFor(() => expect(router.search['list']).toBeUndefined())
  // The sidebar refetched without the deleted list.
  await waitFor(() => expect(screen.queryByText('SaaS founders')).not.toBeInTheDocument())
})
