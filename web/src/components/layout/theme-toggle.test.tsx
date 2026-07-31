import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { makeTestStore, renderWithProviders } from '@/test/render-with-providers'
import { startThemeSync } from '@/lib/theme'
import { ThemeToggle } from './theme-toggle'

function stubSystemDark(matches: boolean) {
  vi.stubGlobal('matchMedia', () => ({
    matches,
    media: '(prefers-color-scheme: dark)',
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

beforeEach(() => {
  document.documentElement.classList.remove('dark')
  stubSystemDark(false)
})

afterEach(() => {
  document.documentElement.classList.remove('dark')
  vi.unstubAllGlobals()
})

test('a theme choice is recorded in the persisted ui slice', () => {
  const { store } = renderWithProviders(<ThemeToggle />)

  fireEvent.click(screen.getByRole('button', { name: 'Use dark theme' }))
  expect(store.getState()).toMatchObject({ ui: { theme: 'dark' } })
  expect(screen.getByRole('button', { name: 'Use light theme' })).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Use light theme' }))
  expect(store.getState()).toMatchObject({ ui: { theme: 'light' } })
})

test('startThemeSync applies the chosen preference to the document', () => {
  const store = makeTestStore()
  renderWithProviders(<ThemeToggle />, { store })
  const stop = startThemeSync(store)

  fireEvent.click(screen.getByRole('button', { name: 'Use dark theme' }))
  expect(document.documentElement).toHaveClass('dark')

  fireEvent.click(screen.getByRole('button', { name: 'Use light theme' }))
  expect(document.documentElement).not.toHaveClass('dark')
  stop()
})

test('an explicit light choice survives the OS reporting dark', () => {
  // The regression this guards: applying the media-query result blindly let an
  // OS-level preference silently overwrite a choice the user had already made.
  stubSystemDark(true)
  const store = makeTestStore()
  renderWithProviders(<ThemeToggle />, { store })
  const stop = startThemeSync(store)

  // 'system' + OS dark resolves to dark, so the offered action is "use light".
  fireEvent.click(screen.getByRole('button', { name: 'Use light theme' }))
  expect(store.getState()).toMatchObject({ ui: { theme: 'light' } })
  expect(document.documentElement).not.toHaveClass('dark')
  stop()
})
