import { screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
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
