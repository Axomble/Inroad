import { fireEvent, render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'
import { GoogleCallbackBanner } from '../google-callback-banner'

// A failed Google sign-in has no in-page mutation to report it: the only signal is
// a query param on the URL the callback redirected to. If this banner misses it,
// the user is dropped back on the login screen with no idea what happened.

test('a failure reason is reported in plain, actionable copy', () => {
  render(<GoogleCallbackBanner reason="denied" onClear={vi.fn()} />)

  const alert = screen.getByRole('alert')
  expect(alert).toHaveTextContent(/cancelled google sign-in/i)
  expect(alert).toHaveTextContent(/sign in with your password/i)
})

test('a mis-configured server says so, rather than blaming the user', () => {
  render(<GoogleCallbackBanner reason="disabled" onClear={vi.fn()} />)
  expect(screen.getByRole('alert')).toHaveTextContent(/isn't configured on this server/i)
})

// The one reason worth its own copy. Retrying cannot fix it, so the copy must not
// say "try again" — it has to point at the thing the user can actually change.
test('an unverified Google address explains why retrying will not help', () => {
  render(<GoogleCallbackBanner reason="email_unverified" onClear={vi.fn()} />)

  const alert = screen.getByRole('alert')
  expect(alert).toHaveTextContent(/hasn't verified that account's email address/i)
  expect(alert).toHaveTextContent(/use another account/i)
  expect(alert).not.toHaveTextContent(/try again/i)
})

// These two are different failures and must not share words: one means Google sent
// no address, the other means Google sent one it has not verified. Collapsing them
// would tell a user to "try another account" when the real fix is verifying this one.
test('no address and an unverified address read as different problems', () => {
  const { unmount } = render(<GoogleCallbackBanner reason="no_email" onClear={vi.fn()} />)
  const noEmail = screen.getByRole('alert').textContent
  unmount()

  render(<GoogleCallbackBanner reason="email_unverified" onClear={vi.fn()} />)
  expect(screen.getByRole('alert').textContent).not.toBe(noEmail)
  expect(noEmail).toMatch(/didn't share an email address/i)
})

test('an expired invite points at getting a new one', () => {
  render(<GoogleCallbackBanner reason="invite_invalid" onClear={vi.fn()} />)
  expect(screen.getByRole('alert')).toHaveTextContent(/ask for a new invite/i)
})

test('an unrecognised reason falls back to a generic message, never the raw code', () => {
  render(<GoogleCallbackBanner reason="something_new_from_the_backend" onClear={vi.fn()} />)

  const alert = screen.getByRole('alert')
  expect(alert).toHaveTextContent(/couldn't finish google sign-in/i)
  expect(alert).not.toHaveTextContent(/something_new_from_the_backend/)
})

test('no reason renders nothing', () => {
  render(<GoogleCallbackBanner reason={undefined} onClear={vi.fn()} />)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('the param is cleared once, so a refresh does not re-show a read failure', () => {
  const onClear = vi.fn()
  const { rerender } = render(<GoogleCallbackBanner reason="bad_state" onClear={onClear} />)

  expect(onClear).toHaveBeenCalledTimes(1)

  // The clear strips the param, so the parent re-renders with no reason — the
  // banner must stay visible off its own snapshot instead of blanking itself.
  rerender(<GoogleCallbackBanner reason={undefined} onClear={onClear} />)
  expect(screen.getByRole('alert')).toHaveTextContent(/expired/i)
  expect(onClear).toHaveBeenCalledTimes(1)
})

test('it can be dismissed', () => {
  render(<GoogleCallbackBanner reason="exchange_failed" onClear={vi.fn()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
