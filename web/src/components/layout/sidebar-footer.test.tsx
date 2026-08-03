import { act, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { setSession } from '@/store/slices/auth'
import { SidebarFooter } from './sidebar-footer'

test('renders name, email, and derived initials from the session', () => {
  renderWithProviders(<SidebarFooter />, {
    preloadedState: {
      auth: { status: 'authed', userName: 'Ada Lovelace', userEmail: 'ada@example.com' },
    },
  })

  expect(screen.getByText('Ada Lovelace')).toBeInTheDocument()
  expect(screen.getByText('ada@example.com')).toBeInTheDocument()
  expect(screen.getByText('AL')).toBeInTheDocument()
})

test('falls back to the email as the display line when no name is known', () => {
  renderWithProviders(<SidebarFooter />, {
    preloadedState: { auth: { status: 'authed', userEmail: 'ada@example.com' } },
  })

  // Email carries the line once, not twice.
  expect(screen.getAllByText('ada@example.com')).toHaveLength(1)
})

test('reserves space with skeletons until the session settles — no identity flash', () => {
  renderWithProviders(<SidebarFooter />)

  expect(screen.queryByText('?')).not.toBeInTheDocument()
  const footer = document.querySelector('[data-slot="sidebar-footer"]')
  expect(footer).not.toBeNull()
  expect(footer!.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThanOrEqual(2)
})

// The hard-reload path: the silent-refresh bootstrap dispatches setSession
// from POST /auth/refresh, which is the SPA's ONLY identity source on a
// reload. The footer must leave the skeleton state on that transition — a
// session response without email once left it skeleton-stuck forever.
test('resolves identity when the bootstrap session lands (hard reload path)', () => {
  const { store } = renderWithProviders(<SidebarFooter />)

  const footer = document.querySelector('[data-slot="sidebar-footer"]')!
  expect(footer.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThanOrEqual(2)

  act(() => {
    store.dispatch(
      setSession({
        access_token: 't',
        expires_in: 900,
        user_id: 'u-1',
        active_workspace_id: 'ws-1',
        role: 'owner',
        memberships: [],
        email: 'demo@inroad.test',
      }),
    )
  })

  expect(screen.getByText('demo@inroad.test')).toBeInTheDocument()
  expect(footer.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(0)
})
