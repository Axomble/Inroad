import { act, fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { pushToast } from '@/store/slices/toast'
import { ToastHost } from './toast-host'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

test('a success toast appears, then clears itself', () => {
  const { store } = renderWithProviders(<ToastHost />)

  act(() => {
    store.dispatch(pushToast({ tone: 'ok', text: 'Campaign is live.' }))
  })
  expect(screen.getByText('Campaign is live.')).toBeInTheDocument()

  act(() => {
    vi.advanceTimersByTime(6000)
  })
  expect(screen.queryByText('Campaign is live.')).not.toBeInTheDocument()
})

// A missed success is harmless — the thing succeeded. A missed failure reads as
// "nothing happened", so it must wait to be acknowledged.
test('an error toast never auto-dismisses, and goes only when dismissed', () => {
  const { store } = renderWithProviders(<ToastHost />)

  act(() => {
    store.dispatch(pushToast({ tone: 'error', text: 'Import failed.' }))
  })

  act(() => {
    vi.advanceTimersByTime(60_000)
  })
  expect(screen.getByText('Import failed.')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /dismiss notification/i }))
  expect(screen.queryByText('Import failed.')).not.toBeInTheDocument()
})

test('the stack is capped, dropping the oldest rather than growing without limit', () => {
  const { store } = renderWithProviders(<ToastHost />)

  act(() => {
    for (const n of [1, 2, 3, 4, 5]) store.dispatch(pushToast({ tone: 'info', text: `Notice ${n}` }))
  })

  expect(screen.queryByText('Notice 1')).not.toBeInTheDocument()
  for (const n of [2, 3, 4, 5]) expect(screen.getByText(`Notice ${n}`)).toBeInTheDocument()
})

test('a toast with a link renders it, and following it dismisses the toast', () => {
  const { store } = renderWithProviders(<ToastHost />)

  act(() => {
    store.dispatch(
      pushToast({ tone: 'ok', text: 'Campaign is live.', href: '/app/campaigns/c-1', hrefLabel: 'View campaign' }),
    )
  })

  const link = screen.getByRole('link', { name: 'View campaign' })
  expect(link).toHaveAttribute('href', '/app/campaigns/c-1')

  fireEvent.click(link)
  expect(screen.queryByText('Campaign is live.')).not.toBeInTheDocument()
})

// Two identical calls are two events (two imports finished), not one repeated —
// the id is minted per dispatch precisely so they don't collapse.
test('identical toasts stack instead of deduping', () => {
  const { store } = renderWithProviders(<ToastHost />)

  act(() => {
    store.dispatch(pushToast({ tone: 'ok', text: 'Import finished.' }))
    store.dispatch(pushToast({ tone: 'ok', text: 'Import finished.' }))
  })

  expect(screen.getAllByText('Import finished.')).toHaveLength(2)
})
