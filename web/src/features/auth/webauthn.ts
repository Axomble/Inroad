/**
 * WebAuthn (passkey) ceremony helpers — the one place that bridges the JSON the
 * server speaks and the binary `ArrayBuffer`s the browser credential API demands.
 *
 * The WebAuthn spec puts raw bytes (challenge, credential ids, user handle,
 * signatures) into `BufferSource`s, but JSON can't carry those — so the server
 * base64url-encodes them and expects them base64url-encoded on the way back. All
 * of that (de/serialization + the `navigator.credentials` calls + error mapping)
 * lives here as small pure/near-pure units so it is unit-testable and used
 * identically by registration (Security page) and discoverable login.
 */

/** A JSON object of unknown shape, as the server sends `publicKey` options. */
type Json = Record<string, unknown>

/** base64url string → ArrayBuffer (spec-compliant: `-`/`_`, no padding). */
export function base64urlToBuffer(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  const pad = base64.length % 4 === 0 ? '' : '='.repeat(4 - (base64.length % 4))
  const binary = atob(base64 + pad)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

/** ArrayBuffer → base64url string (no padding), the form the server stores. */
export function bufferToBase64url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * Feature detection. Callers must gate all passkey UI on this — an absent
 * `PublicKeyCredential` means the browser can't run either ceremony, so the
 * "Add a passkey" / "Sign in with a passkey" affordances should not render.
 */
export function isWebAuthnAvailable(): boolean {
  return typeof window !== 'undefined' && typeof window.PublicKeyCredential === 'function'
}

/**
 * Decode the server's registration options into the binary form
 * `navigator.credentials.create` requires: `challenge`, `user.id`, and every
 * `excludeCredentials[].id` arrive as base64url and must become `ArrayBuffer`s.
 */
export function toCreationOptions(publicKey: Json): PublicKeyCredentialCreationOptions {
  const opts: Json = { ...publicKey }
  opts.challenge = base64urlToBuffer(asString(publicKey.challenge))

  const user: Json = { ...asObject(publicKey.user) }
  user.id = base64urlToBuffer(asString(user.id))
  opts.user = user

  if (Array.isArray(publicKey.excludeCredentials)) {
    opts.excludeCredentials = publicKey.excludeCredentials.map((entry) => decodeCredentialDescriptor(entry))
  }
  return opts as unknown as PublicKeyCredentialCreationOptions
}

/**
 * Decode the server's assertion options for `navigator.credentials.get`. For a
 * discoverable (usernameless) login `allowCredentials` is empty, but we still
 * decode any ids the server chose to send so the same helper serves both.
 */
export function toRequestOptions(publicKey: Json): PublicKeyCredentialRequestOptions {
  const opts: Json = { ...publicKey }
  opts.challenge = base64urlToBuffer(asString(publicKey.challenge))
  if (Array.isArray(publicKey.allowCredentials)) {
    opts.allowCredentials = publicKey.allowCredentials.map((entry) => decodeCredentialDescriptor(entry))
  }
  return opts as unknown as PublicKeyCredentialRequestOptions
}

function decodeCredentialDescriptor(entry: unknown): PublicKeyCredentialDescriptor {
  const desc: Json = { ...asObject(entry) }
  desc.id = base64urlToBuffer(asString(desc.id))
  return desc as unknown as PublicKeyCredentialDescriptor
}

/**
 * Serialize the attestation from `navigator.credentials.create` back to the
 * JSON the server verifies: binary fields become base64url. Matches the
 * `go-webauthn` parser's expected shape.
 */
export function encodeRegistrationCredential(credential: PublicKeyCredential): Json {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      attestationObject: bufferToBase64url(response.attestationObject),
      transports: typeof response.getTransports === 'function' ? response.getTransports() : [],
    },
  }
}

/** Serialize the assertion from `navigator.credentials.get` for login/finish. */
export function encodeAuthenticationCredential(credential: PublicKeyCredential): Json {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
      userHandle: response.userHandle ? bufferToBase64url(response.userHandle) : null,
    },
  }
}

/**
 * Run the registration ceremony end to end: decode options, prompt the
 * authenticator, and re-encode the attestation. Throws a DOMException on user
 * cancel / no authenticator — callers map it via {@link webauthnErrorMessage}.
 */
export async function runRegistrationCeremony(publicKey: Json): Promise<Json> {
  const credential = await navigator.credentials.create({ publicKey: toCreationOptions(publicKey) })
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('No credential was created.')
  }
  return encodeRegistrationCredential(credential)
}

/** Run the discoverable-login ceremony end to end. Throws like the above. */
export async function runAuthenticationCeremony(publicKey: Json): Promise<Json> {
  const credential = await navigator.credentials.get({ publicKey: toRequestOptions(publicKey) })
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('No credential was returned.')
  }
  return encodeAuthenticationCredential(credential)
}

/**
 * Map a thrown ceremony error to a user-facing sentence. The credential API
 * signals every distinct failure (cancel, timeout, duplicate, insecure origin)
 * through a `DOMException` name, so we never leave a stuck spinner with no
 * explanation.
 */
export function webauthnErrorMessage(err: unknown): string {
  // A `DOMException` carries the meaningful `name` but is NOT an `Error`
  // subclass in every runtime, so read `name` structurally rather than gating
  // on `instanceof Error`.
  switch (errorName(err)) {
    case 'NotAllowedError':
      return 'The request was cancelled or timed out. Please try again.'
    case 'InvalidStateError':
      return 'This device already has a passkey for your account.'
    case 'NotSupportedError':
      return "This device doesn't support passkeys."
    case 'SecurityError':
      return 'Passkeys require a secure (HTTPS) connection.'
    case 'AbortError':
      return 'The request was cancelled.'
    default:
      return 'Something went wrong with your passkey. Please try again.'
  }
}

function errorName(err: unknown): string | null {
  if (typeof err === 'object' && err !== null && 'name' in err) {
    const { name } = err as { name: unknown }
    if (typeof name === 'string') return name
  }
  return null
}

function asString(value: unknown): string {
  if (typeof value !== 'string') throw new TypeError('Expected a base64url string in the WebAuthn options.')
  return value
}

function asObject(value: unknown): Json {
  if (typeof value !== 'object' || value === null) {
    throw new TypeError('Expected an object in the WebAuthn options.')
  }
  return value as Json
}
