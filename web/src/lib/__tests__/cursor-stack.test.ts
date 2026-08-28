import { expect, test } from 'vitest'
import { popCursor, pushCursor, type CursorStack } from '../cursor-stack'

// The one thing a keyset pager cannot derive is where it came from, so this
// stack IS the Previous button. Everything below is stated as a walk a user
// takes — forward, back, and off the end — rather than as the array that
// happens to record it.

test('Previous walks back through the pages Next walked forward, ending on the cursor-less first page', () => {
  // Page 1 (no cursor) → Next → page 2 (reached via 'cur-2') → Next → page 3.
  let stack: CursorStack = pushCursor([], undefined)
  stack = pushCursor(stack, 'cur-2')

  // Previous returns the cursor that fetched page 2 — the page just left.
  const toPageTwo = popCursor(stack)
  expect(toPageTwo.cursor).toBe('cur-2')

  // Previous again lands on page 1, which is `undefined` rather than a cursor:
  // the first page is a request with no cursor at all, and sending the empty
  // string the floor is stored as would be sending a malformed one.
  const toPageOne = popCursor(toPageTwo.stack)
  expect(toPageOne.cursor).toBeUndefined()

  // Home again: nothing left to walk back to, which is what disables Previous.
  expect(toPageOne.stack).toHaveLength(0)
})

test('the stack depth is the number of pages walked, which is what numbers the rows', () => {
  // The contacts pager renders "101–150 of 2,340" from this depth, so leaving
  // page 1 has to count — the cursor-less first page is a page walked, not a
  // page skipped because it had no cursor to record.
  const afterOneNext = pushCursor([], undefined)
  expect(afterOneNext).toHaveLength(1)

  const afterTwoNexts = pushCursor(afterOneNext, 'cur-2')
  expect(afterTwoNexts).toHaveLength(2)

  expect(popCursor(afterTwoNexts).stack).toHaveLength(1)
})

test('popping an empty stack is answerable, not an error: a deep link has no history', () => {
  // A pasted `?cursor=…` opens on page forty with nothing behind it. The stack
  // has to say "I have nowhere to send you" so the caller can fall back to its
  // own recovery (contacts has a prev_cursor; the inbox only has page one).
  const nowhere = popCursor([])
  expect(nowhere.cursor).toBeUndefined()
  expect(nowhere.stack).toHaveLength(0)

  // And it stays answerable — popping past the floor never goes negative.
  expect(popCursor(nowhere.stack).cursor).toBeUndefined()
})


test('a cursor is stored and returned verbatim, whatever the list encoded into it', () => {
  // This is what lets one stack serve both lists: contacts pages on an opaque
  // server token, the inbox on a locally packed `last_message_at::id` pair, and
  // neither is ever parsed here.
  const opaqueToken = 'eyJpZCI6ImMtOTkiLCJzb3J0IjoibmV3ZXN0In0='
  const packedPair = '2026-08-05T10:00:00Z::t-24'

  const stack = pushCursor(pushCursor([], opaqueToken), packedPair)
  const first = popCursor(stack)
  expect(first.cursor).toBe(packedPair)
  expect(popCursor(first.stack).cursor).toBe(opaqueToken)
})

test('walking copies the stack instead of mutating it, so React sees each move', () => {
  // The pages hold this in useState. An in-place push would be an update React
  // cannot detect (same reference), leaving a pager whose Previous silently
  // stops matching the page on screen.
  const atPageTwo = pushCursor([], undefined)

  const atPageThree = pushCursor(atPageTwo, 'cur-2')
  expect(atPageThree).not.toBe(atPageTwo)
  expect(atPageTwo).toHaveLength(1)

  const popped = popCursor(atPageThree)
  expect(popped.stack).not.toBe(atPageThree)
  expect(atPageThree).toHaveLength(2)
})
