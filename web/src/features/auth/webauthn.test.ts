import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  base64urlToBuffer,
  bufferToBase64url,
  isWebAuthnAvailable,
  toCreationOptions,
  toRequestOptions,
  webauthnErrorMessage,
} from './webauthn'

function toArray(buffer: ArrayBuffer): number[] {
  return Array.from(new Uint8Array(buffer))
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
