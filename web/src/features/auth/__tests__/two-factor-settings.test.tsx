import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { TwoFactorSettings } from '../two-factor-settings'

// Radix AlertDialog uses pointer + scroll APIs jsdom doesn't implement; polyfill
// what it touches so the enroll/disable dialogs can open (same shim the
// active-sessions test uses). And stub the QR renderer's dynamic import so the
// test doesn't depend on the `qrcode` package's output.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

// The QR renderer lazy-loads the `qrcode` package via a dynamic import; stub it
// so the enrollment step is deterministic and doesn't pull the heavy dependency
// into the test. The plaintext secret is the assertion target either way.
vi.mock('@/features/auth/qr-code', () => ({
  QrCode: () => null,
}))

const jsonHeaders = { 'content-type': 'application/json' }

// Mutable server state + per-test overridable responders.
let totpEnabled: boolean
let recoveryRemaining: number
let confirmResponder: () => Response
let disableResponder: () => Response

const RECOVERY_CODES = ['aaaa-1111', 'bbbb-2222', 'cccc-3333', 'dddd-4444', 'eeee-5555']

beforeEach(() => {
  totpEnabled = false
  recoveryRemaining = 0

  confirmResponder = () =>
    new Response(JSON.stringify({ recovery_codes: RECOVERY_CODES }), { status: 200, headers: jsonHeaders })
  disableResponder = () => new Response(null, { status: 204 })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (url.includes('/auth/2fa/totp/confirm')) {
        const res = confirmResponder()
        if (res.status === 200) {
          totpEnabled = true
          recoveryRemaining = RECOVERY_CODES.length
        }
        return res
      }
      if (url.includes('/auth/2fa/totp')) {
        if (method === 'DELETE') {
          const res = disableResponder()
          if (res.status === 204) {
            totpEnabled = false
            recoveryRemaining = 0
          }
          return res
        }
        return new Response(
          JSON.stringify({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/Inroad:me?secret=JBSWY3DPEHPK3PXP' }),
          { status: 200, headers: jsonHeaders },
        )
      }
      // GET /auth/2fa status
      return new Response(
        JSON.stringify({ totp_enabled: totpEnabled, recovery_codes_remaining: recoveryRemaining }),
        { status: 200, headers: jsonHeaders },
      )
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders the disabled status with an enable action', async () => {
  renderWithProviders(<TwoFactorSettings />)

  expect(await screen.findByText('Not enabled')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /enable 2fa/i })).toBeInTheDocument()
})

test('renders the enabled status with remaining recovery codes', async () => {
  totpEnabled = true
  recoveryRemaining = 7

  renderWithProviders(<TwoFactorSettings />)

  expect(await screen.findByText('Enabled')).toBeInTheDocument()
  expect(screen.getByText(/7 recovery codes remaining/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /disable/i })).toBeInTheDocument()
})

test('enroll → confirm shows the recovery codes once, gated behind an acknowledgement', async () => {
  renderWithProviders(<TwoFactorSettings />)

  fireEvent.click(await screen.findByRole('button', { name: /enable 2fa/i }))

  // The enrollment secret and QR render; enter a code and verify.
  expect(await screen.findByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText(/6-digit code/i), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: /verify & enable/i }))

  // Recovery codes appear exactly once. "Done" is blocked until acknowledged.
  expect(await screen.findByText('aaaa-1111')).toBeInTheDocument()
  const done = screen.getByRole('button', { name: /^done$/i })
  expect(done).toBeDisabled()

  fireEvent.click(screen.getByLabelText(/i've saved these recovery codes/i))
  expect(done).toBeEnabled()

  fireEvent.click(done)

  // Dialog closes and the success notice shows.
  await waitFor(() => expect(screen.queryByText('aaaa-1111')).not.toBeInTheDocument())
  expect(screen.getByRole('status')).toHaveTextContent(/two-factor authentication is on/i)
})

test('confirm surfaces an inline error on a wrong code', async () => {
  confirmResponder = () => new Response(JSON.stringify({ error: 'invalid_code' }), { status: 400, headers: jsonHeaders })

  renderWithProviders(<TwoFactorSettings />)
  fireEvent.click(await screen.findByRole('button', { name: /enable 2fa/i }))

  await screen.findByText('JBSWY3DPEHPK3PXP')
  fireEvent.change(screen.getByLabelText(/6-digit code/i), { target: { value: '000000' } })
  fireEvent.click(screen.getByRole('button', { name: /verify & enable/i }))

  expect(await screen.findByText(/that code didn't match/i)).toBeInTheDocument()
  // Still on the scan step — recovery codes were never shown.
  expect(screen.queryByText('aaaa-1111')).not.toBeInTheDocument()
})

test('disable requires a code and confirms on success', async () => {
  totpEnabled = true
  recoveryRemaining = 8

  renderWithProviders(<TwoFactorSettings />)
  fireEvent.click(await screen.findByRole('button', { name: /^disable$/i }))

  // The confirm button is inert until a code is entered.
  const confirm = await screen.findByRole('button', { name: /disable 2fa/i })
  expect(confirm).toBeDisabled()

  fireEvent.change(screen.getByLabelText(/verification code/i), { target: { value: '654321' } })
  expect(confirm).toBeEnabled()
  fireEvent.click(confirm)

  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent(/two-factor authentication is off/i),
  )
})

test('disable shows an error when the code is rejected', async () => {
  totpEnabled = true
  recoveryRemaining = 8
  disableResponder = () => new Response(JSON.stringify({ error: 'invalid_code' }), { status: 401, headers: jsonHeaders })

  renderWithProviders(<TwoFactorSettings />)
  fireEvent.click(await screen.findByRole('button', { name: /^disable$/i }))

  fireEvent.change(await screen.findByLabelText(/verification code/i), { target: { value: '000000' } })
  fireEvent.click(screen.getByRole('button', { name: /disable 2fa/i }))

  expect(await screen.findByText(/that code was incorrect/i)).toBeInTheDocument()
})
