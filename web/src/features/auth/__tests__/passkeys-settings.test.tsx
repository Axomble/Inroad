import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { PasskeysSettings } from '../passkeys-settings'

// Radix AlertDialog touches pointer + scroll APIs jsdom doesn't implement.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

// A minimal PublicKeyCredential stand-in: `runRegistrationCeremony` narrows the
// ceremony result with `instanceof PublicKeyCredential`, so the created object
// must be an instance of the same (stubbed) global.
class FakePublicKeyCredential {
  id = 'cred-abc'
  type = 'public-key'
  rawId = new Uint8Array([1, 2, 3]).buffer
  authenticatorAttachment: string | null = 'platform'
  response = {
    clientDataJSON: new Uint8Array([4, 5, 6]).buffer,
    attestationObject: new Uint8Array([7, 8, 9]).buffer,
    getTransports: () => ['internal'],
  }
  getClientExtensionResults() {
    return {}
  }
}

const BEGIN_OPTIONS = {
  session_id: 'sess-1',
  publicKey: {
    challenge: 'AAEC',
    rp: { name: 'Inroad', id: 'localhost' },
    user: { id: 'AAEC', name: 'me@company.com', displayName: 'Me' },
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
  },
}

type Passkey = { id: string; label: string; transports: string[]; created_at: string; last_used_at: string | null }

let passkeys: Passkey[]
let createMock: ReturnType<typeof vi.fn>
// Per-test overridable responders for the two-step registration ceremony.
let beginResponder: () => Response
let finishResponder: () => Response

beforeEach(() => {
  passkeys = []

  beginResponder = () => new Response(JSON.stringify(BEGIN_OPTIONS), { status: 200, headers: jsonHeaders })
  finishResponder = () => {
    passkeys = [
      { id: 'p1', label: 'MacBook', transports: ['internal'], created_at: new Date().toISOString(), last_used_at: null },
    ]
    return new Response(null, { status: 204 })
  }

  createMock = vi.fn().mockResolvedValue(new FakePublicKeyCredential())
  vi.stubGlobal('PublicKeyCredential', FakePublicKeyCredential)
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { create: createMock, get: vi.fn() },
  })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (url.includes('/auth/passkeys/register/begin')) {
        return beginResponder()
      }
      if (url.includes('/auth/passkeys/register/finish')) {
        return finishResponder()
      }
      if (url.includes('/auth/passkeys/')) {
        if (method === 'DELETE') {
          passkeys = []
          return new Response(null, { status: 204 })
        }
      }
      // GET /auth/passkeys
      return new Response(JSON.stringify({ passkeys }), { status: 200, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders nothing when the browser has no WebAuthn support', () => {
  vi.stubGlobal('PublicKeyCredential', undefined)
  const { container } = renderWithProviders(<PasskeysSettings />)
  expect(container).toBeEmptyDOMElement()
})

test('shows the empty state and an add action when supported', async () => {
  renderWithProviders(<PasskeysSettings />)
  expect(await screen.findByText('No passkeys yet')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /add a passkey/i })).toBeInTheDocument()
})

test('add flow runs the registration ceremony and surfaces success', async () => {
  renderWithProviders(<PasskeysSettings />)

  await screen.findByText('No passkeys yet')
  fireEvent.click(screen.getByRole('button', { name: /add a passkey/i }))
  fireEvent.change(screen.getByLabelText(/passkey name/i), { target: { value: 'MacBook' } })
  fireEvent.click(screen.getByRole('button', { name: /create passkey/i }))

  // The browser ceremony ran with decoded (binary) options.
  await waitFor(() => expect(createMock).toHaveBeenCalledTimes(1))
  const passedOptions = createMock.mock.calls[0]?.[0] as { publicKey: PublicKeyCredentialCreationOptions }
  expect(passedOptions.publicKey.challenge).toBeInstanceOf(ArrayBuffer)
  expect(passedOptions.publicKey.user.id).toBeInstanceOf(ArrayBuffer)

  // Success notice, and the refetched list now shows the new passkey.
  expect(await screen.findByRole('status')).toHaveTextContent(/is ready to use/i)
  expect(await screen.findByText('MacBook')).toBeInTheDocument()
})

test('a cancelled ceremony shows an inline error, not a stuck spinner', async () => {
  createMock.mockRejectedValueOnce(new DOMException('cancelled', 'NotAllowedError'))

  renderWithProviders(<PasskeysSettings />)
  await screen.findByText('No passkeys yet')
  fireEvent.click(screen.getByRole('button', { name: /add a passkey/i }))
  fireEvent.change(screen.getByLabelText(/passkey name/i), { target: { value: 'Key' } })
  fireEvent.click(screen.getByRole('button', { name: /create passkey/i }))

  expect(await screen.findByText(/cancelled or timed out/i)).toBeInTheDocument()
  // The create button is interactive again (no stuck busy state).
  expect(screen.getByRole('button', { name: /create passkey/i })).toBeEnabled()
})

test('a 501 from register/begin shows the not-configured message, not a generic retry', async () => {
  beginResponder = () =>
    new Response(JSON.stringify({ error: 'not_configured' }), { status: 501, headers: jsonHeaders })

  renderWithProviders(<PasskeysSettings />)
  await screen.findByText('No passkeys yet')
  fireEvent.click(screen.getByRole('button', { name: /add a passkey/i }))
  fireEvent.change(screen.getByLabelText(/passkey name/i), { target: { value: 'MacBook' } })
  fireEvent.click(screen.getByRole('button', { name: /create passkey/i }))

  expect(await screen.findByText(/not configured on this server/i)).toBeInTheDocument()
  expect(screen.queryByText(/couldn't start passkey setup/i)).not.toBeInTheDocument()
  // The ceremony never ran (begin failed first) and the button is usable again.
  expect(createMock).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: /create passkey/i })).toBeEnabled()
})

test('a 501 from register/finish shows the not-configured message, not a generic retry', async () => {
  finishResponder = () =>
    new Response(JSON.stringify({ error: 'not_configured' }), { status: 501, headers: jsonHeaders })

  renderWithProviders(<PasskeysSettings />)
  await screen.findByText('No passkeys yet')
  fireEvent.click(screen.getByRole('button', { name: /add a passkey/i }))
  fireEvent.change(screen.getByLabelText(/passkey name/i), { target: { value: 'MacBook' } })
  fireEvent.click(screen.getByRole('button', { name: /create passkey/i }))

  // The ceremony ran, but the finish call reported passkeys aren't configured.
  await waitFor(() => expect(createMock).toHaveBeenCalledTimes(1))
  expect(await screen.findByText(/not configured on this server/i)).toBeInTheDocument()
  expect(screen.queryByText(/that didn't work/i)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: /create passkey/i })).toBeEnabled()
})

test('removing a passkey confirms and updates the list', async () => {
  passkeys = [
    { id: 'p1', label: 'YubiKey', transports: [], created_at: new Date().toISOString(), last_used_at: null },
  ]

  renderWithProviders(<PasskeysSettings />)

  fireEvent.click(await screen.findByRole('button', { name: /remove passkey yubikey/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^remove passkey$/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/“yubikey” was removed/i))
})
