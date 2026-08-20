import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { GoogleSigninButton } from '../google-signin-button'

// Google sign-in is the one method that does NOT resolve through a mutation: the
// start route 302s straight to Google, so the button hands the top-level browser to
// it. What these tests pin is the navigation and the URL it carries — there is no
// request body, and deliberately no capability probe on the critical path.

const ORIGINAL_LOCATION = window.location
let assign: ReturnType<typeof vi.fn>

beforeEach(() => {
  assign = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...ORIGINAL_LOCATION, origin: ORIGINAL_LOCATION.origin, assign },
  })
})

afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
  vi.restoreAllMocks()
})

test('renders with the given label', () => {
  render(<GoogleSigninButton label="Continue with Google" />)
  expect(screen.getByRole('button', { name: 'Continue with Google' })).toBeInTheDocument()
})

test('clicking navigates the browser to the backend start route', () => {
  render(<GoogleSigninButton label="Continue with Google" />)
  fireEvent.click(screen.getByRole('button', { name: 'Continue with Google' }))

  // A top-level navigation, not a fetch — the start route redirects to Google and
  // the callback sets the session cookie server-side.
  expect(assign).toHaveBeenCalledTimes(1)
  expect(assign.mock.calls[0]?.[0]).toBe('http://localhost:5173/api/v1/auth/oauth/google/start')
})

test('a safe return_to rides along so the callback can resume it', () => {
  render(<GoogleSigninButton label="Continue with Google" returnTo="/oauth/consent?consent_id=c-1" />)
  fireEvent.click(screen.getByRole('button', { name: 'Continue with Google' }))

  expect(assign.mock.calls[0]?.[0]).toBe(
    'http://localhost:5173/api/v1/auth/oauth/google/start?return_to=%2Foauth%2Fconsent%3Fconsent_id%3Dc-1',
  )
})

// The server validates this too, but an off-origin value must not leave this origin
// in the first place. Same bypass families the login form's resume guards against.
test.each([
  '//evil.com',
  '/\\evil.com',
  '/\\/evil.com',
  'https://evil.com',
  'javascript:alert(1)',
  'data:text/html,x',
])('an unsafe return_to (%j) is stripped from the start URL', (returnTo) => {
  render(<GoogleSigninButton label="Continue with Google" returnTo={returnTo} />)
  fireEvent.click(screen.getByRole('button', { name: 'Continue with Google' }))

  expect(assign.mock.calls[0]?.[0]).toBe('http://localhost:5173/api/v1/auth/oauth/google/start')
})
