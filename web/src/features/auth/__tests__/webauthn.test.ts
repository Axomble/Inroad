import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  base64urlToBuffer,
  bufferToBase64url,
  encodeAuthenticationCredential,
  encodeRegistrationCredential,
  isWebAuthnAvailable,
  toCreationOptions,
  toRequestOptions,
  webauthnErrorMessage,
} from '../webauthn'

function toArray(buffer: ArrayBuffer): number[] {
  return Array.from(new Uint8Array(buffer))
}

function bytes(values: number[]): ArrayBuffer {
  return new Uint8Array(values).buffer
}

describe('base64url <-> ArrayBuffer', () => {
  test('round-trips arbitrary byte sequences losslessly', () => {
    for (let len = 0; len <= 40; len += 1) {
      const source = new Uint8Array(len)
      for (let i = 0; i < len; i += 1) source[i] = (i * 37 + len) % 256

      const encoded = bufferToBase64url(source.buffer)
      // URL-safe alphabet, no padding.
      expect(encoded).not.toMatch(/[+/=]/)

      expect(toArray(base64urlToBuffer(encoded))).toEqual(Array.from(source))
    }
  })

  test('decodes a known url-safe value (0xff 0xfe 0xfd -> "__79")', () => {
    expect(toArray(base64urlToBuffer('__79'))).toEqual([0xff, 0xfe, 0xfd])
  })

  test('decodes an unpadded value ("AAEC" -> 0x00 0x01 0x02)', () => {
    expect(toArray(base64urlToBuffer('AAEC'))).toEqual([0, 1, 2])
  })
})

describe('toCreationOptions', () => {
  test('decodes challenge, user.id, and excludeCredentials ids into ArrayBuffers', () => {
    const opts = toCreationOptions({
      challenge: '__79',
      rp: { name: 'Inroad' },
      user: { id: 'AAEC', name: 'a@b.com', displayName: 'A B' },
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      excludeCredentials: [{ id: '__79', type: 'public-key', transports: ['internal'] }],
    })

    expect(opts.challenge).toBeInstanceOf(ArrayBuffer)
    expect(toArray(opts.challenge as ArrayBuffer)).toEqual([0xff, 0xfe, 0xfd])

    expect(opts.user.id).toBeInstanceOf(ArrayBuffer)
    expect(toArray(opts.user.id as ArrayBuffer)).toEqual([0, 1, 2])
    // Non-binary fields pass through untouched.
    expect(opts.user.name).toBe('a@b.com')

    const [descriptor] = opts.excludeCredentials ?? []
    expect(descriptor?.id).toBeInstanceOf(ArrayBuffer)
    expect(descriptor?.type).toBe('public-key')
    expect(descriptor?.transports).toEqual(['internal'])
  })
})

describe('toRequestOptions', () => {
  test('decodes the challenge and leaves an empty allowCredentials for discoverable login', () => {
    const opts = toRequestOptions({ challenge: 'AAEC', allowCredentials: [], userVerification: 'required' })
    expect(toArray(opts.challenge as ArrayBuffer)).toEqual([0, 1, 2])
    expect(opts.allowCredentials).toEqual([])
    expect(opts.userVerification).toBe('required')
  })
})

describe('encodeRegistrationCredential', () => {
  test('re-encodes rawId, clientDataJSON, and attestationObject to base64url', () => {
    // 0xff 0xfe 0xfd -> "__79" exercises the URL-safe alphabet on the way out.
    const credential = {
      id: 'cred-id',
      type: 'public-key',
      rawId: bytes([0xff, 0xfe, 0xfd]),
      authenticatorAttachment: 'platform',
      getClientExtensionResults: () => ({}),
      response: {
        clientDataJSON: bytes([0, 1, 2]),
        attestationObject: bytes([10, 20, 30, 40, 50]),
        getTransports: () => ['internal', 'hybrid'],
      },
    } as unknown as PublicKeyCredential

    const out = encodeRegistrationCredential(credential) as {
      id: string
      rawId: string
      type: string
      authenticatorAttachment?: string
      response: { clientDataJSON: string; attestationObject: string; transports: string[] }
    }

    expect(out.id).toBe('cred-id')
    expect(out.type).toBe('public-key')
    expect(out.authenticatorAttachment).toBe('platform')

    // Exact base64url for the known inputs — a regression to standard base64
    // (with +/ or padding) would break this.
    expect(out.rawId).toBe('__79')
    expect(out.response.clientDataJSON).toBe('AAEC')
    expect(out.rawId).not.toMatch(/[+/=]/)

    // Round-trip the attestation bytes to prove a lossless re-encode.
    expect(toArray(base64urlToBuffer(out.response.attestationObject))).toEqual([10, 20, 30, 40, 50])
    expect(out.response.transports).toEqual(['internal', 'hybrid'])
  })

  test('falls back to an empty transports list when getTransports is unavailable', () => {
    const credential = {
      id: 'cred-id',
      type: 'public-key',
      rawId: bytes([1]),
      getClientExtensionResults: () => ({}),
      response: {
        clientDataJSON: bytes([2]),
        attestationObject: bytes([3]),
        // No getTransports (older authenticators / Safari).
      },
    } as unknown as PublicKeyCredential

    const out = encodeRegistrationCredential(credential) as { response: { transports: string[] } }
    expect(out.response.transports).toEqual([])
  })
})

describe('encodeAuthenticationCredential', () => {
  test('re-encodes authenticatorData, signature, and a present userHandle to base64url', () => {
    const credential = {
      id: 'cred-id',
      type: 'public-key',
      rawId: bytes([0xff, 0xfe, 0xfd]),
      authenticatorAttachment: 'cross-platform',
      getClientExtensionResults: () => ({}),
      response: {
        clientDataJSON: bytes([0, 1, 2]),
        authenticatorData: bytes([9, 8, 7, 6]),
        signature: bytes([255, 0, 128]),
        userHandle: bytes([0, 1, 2]),
      },
    } as unknown as PublicKeyCredential

    const out = encodeAuthenticationCredential(credential) as {
      rawId: string
      response: {
        clientDataJSON: string
        authenticatorData: string
        signature: string
        userHandle: string | null
      }
    }

    expect(out.rawId).toBe('__79')
    expect(out.response.clientDataJSON).toBe('AAEC')
    expect(out.response.userHandle).toBe('AAEC')
    // Round-trip the binary fields — proves the bytes survive intact.
    expect(toArray(base64urlToBuffer(out.response.authenticatorData))).toEqual([9, 8, 7, 6])
    expect(toArray(base64urlToBuffer(out.response.signature))).toEqual([255, 0, 128])
    expect(out.response.signature).not.toMatch(/[+/=]/)
  })

  test('serializes userHandle to null when the authenticator returns none', () => {
    const credential = {
      id: 'cred-id',
      type: 'public-key',
      rawId: bytes([1, 2, 3]),
      getClientExtensionResults: () => ({}),
      response: {
        clientDataJSON: bytes([4, 5, 6]),
        authenticatorData: bytes([7, 8, 9]),
        signature: bytes([10, 11, 12]),
        userHandle: null,
      },
    } as unknown as PublicKeyCredential

    const out = encodeAuthenticationCredential(credential) as { response: { userHandle: string | null } }
    expect(out.response.userHandle).toBeNull()
  })
})

describe('webauthnErrorMessage', () => {
  test('maps ceremony DOMException names to distinct, user-facing messages', () => {
    expect(webauthnErrorMessage(new DOMException('x', 'NotAllowedError'))).toMatch(/cancelled or timed out/i)
    expect(webauthnErrorMessage(new DOMException('x', 'InvalidStateError'))).toMatch(/already has a passkey/i)
    expect(webauthnErrorMessage(new DOMException('x', 'NotSupportedError'))).toMatch(/doesn't support passkeys/i)
    expect(webauthnErrorMessage(new DOMException('x', 'SecurityError'))).toMatch(/secure \(https\)/i)
  })

  test('falls back to a generic message for unknown errors', () => {
    expect(webauthnErrorMessage('nope')).toMatch(/something went wrong/i)
    expect(webauthnErrorMessage(new Error('boom'))).toMatch(/something went wrong/i)
  })
})

describe('isWebAuthnAvailable', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  test('is false when the browser exposes no PublicKeyCredential', () => {
    // jsdom does not implement WebAuthn, so it is absent by default.
    expect(isWebAuthnAvailable()).toBe(false)
  })

  test('is true once PublicKeyCredential is present', () => {
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    expect(isWebAuthnAvailable()).toBe(true)
  })
})
