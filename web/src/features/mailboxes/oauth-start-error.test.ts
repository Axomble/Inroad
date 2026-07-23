import { startErrorCopy, startErrorKind } from './oauth-start-error'

// The Gmail "start" endpoint reports a mis-configured server as 501; every
// other outcome is a transient failure. Mirrors the branch used in
// mailboxes-page's onConnectGmail.
test('a 501 maps to the "disabled" kind', () => {
  expect(startErrorKind({ status: 501, data: undefined })).toBe('disabled')
})

test('other HTTP statuses map to "generic"', () => {
  expect(startErrorKind({ status: 500, data: undefined })).toBe('generic')
  expect(startErrorKind({ status: 429, data: undefined })).toBe('generic')
})

test('a network error (string status tag) maps to "generic"', () => {
  expect(startErrorKind({ status: 'FETCH_ERROR', error: 'boom' })).toBe('generic')
})

test('an absent error maps to "generic"', () => {
  expect(startErrorKind(undefined)).toBe('generic')
})

test('copy is defined for both kinds', () => {
  expect(startErrorCopy.disabled).toMatch(/configured/i)
  expect(startErrorCopy.generic).toMatch(/try again/i)
})
