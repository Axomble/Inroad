import { expect, test } from 'vitest'
import {
  decodeCursor,
  encodeCursor,
  isStaleCursorError,
  parseInboxSearch,
} from '../inbox-search'

// The inbox's keyset cursor is two real values taken off the last row of
// whatever page is on screen (last_message_at, id) — there is no server-
// issued opaque token to round-trip against, so packing/unpacking that pair
// correctly is the whole ballgame for "does paging forward and back work".

test('parses a well-formed URL into the search contract', () => {
  expect(parseInboxSearch({ mailbox: 'mb-1', class: 'positive', q: 'acme', cursor: 'c1' })).toEqual({
    mailbox: 'mb-1',
    class: 'positive',
    q: 'acme',
    cursor: 'c1',
  })
})

test('drops params that are missing or the wrong type, rather than forwarding garbage', () => {
  expect(parseInboxSearch({ mailbox: '', class: 42, q: undefined })).toEqual({
    mailbox: undefined,
    class: undefined,
    q: undefined,
    cursor: undefined,
  })
})

test('a cursor round-trips through encode/decode unchanged', () => {
  const lastMessageAt = '2026-08-05T10:00:00Z'
  const id = 't-24'
  const encoded = encodeCursor(lastMessageAt, id)
  expect(decodeCursor(encoded)).toEqual({ beforeLastMessageAt: lastMessageAt, beforeId: id })
})

test('decode returns undefined for a missing, half-set, or malformed cursor', () => {
  expect(decodeCursor(undefined)).toBeUndefined()
  expect(decodeCursor('')).toBeUndefined()
  // No separator at all.
  expect(decodeCursor('not-a-real-cursor')).toBeUndefined()
  // Separator present but one side empty.
  expect(decodeCursor('::t-1')).toBeUndefined()
  expect(decodeCursor('2026-08-05T10:00:00Z::')).toBeUndefined()
})

test('a cursor the server rejects (400) is recognised as stale; anything else is a real error', () => {
  expect(isStaleCursorError({ status: 400, data: {} })).toBe(true)
  expect(isStaleCursorError({ status: 422, data: {} })).toBe(false)
  expect(isStaleCursorError({ status: 500, data: {} })).toBe(false)
  expect(isStaleCursorError({ name: 'Error', message: 'offline' })).toBe(false)
})
