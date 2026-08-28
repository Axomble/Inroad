/**
 * The visited-page stack that gives keyset pagination a Previous button.
 *
 * A keyset response can name the page *after* the one on screen, but nothing in
 * it can name "page N back" — so a list that pages this way has to remember the
 * pages it already left. The contacts list and the inbox grew the same few lines
 * independently and a third list needed them too, which is the point at which
 * the rule earns one home instead of a copy per feature.
 *
 * Deliberately ignorant of what a cursor *is*. Contacts pages on an opaque token
 * the server minted; the inbox pages on a locally packed `last_message_at::id`
 * pair. A stack that only ever stores and hands back the string it was given
 * cannot care which, and that is exactly why one implementation serves both.
 *
 * Pure — no React, no fetching, no domain knowledge — so the paging rule is
 * unit-tested directly rather than through a rendered pager.
 */
export type CursorStack = readonly string[]

/**
 * Records the page being left, so Previous can return to it.
 *
 * `undefined` is the cursor-less first page and is stored as the empty string:
 * the floor of the stack has to be *remembered* (it is what makes the second
 * page's Previous land on the first) while still round-tripping back to "no
 * cursor" rather than to a cursor that never existed.
 *
 * Copies rather than mutates — the callers hold the stack in React state, where
 * an in-place push would be an update React cannot see.
 */
export function pushCursor(stack: CursorStack, current: string | undefined): CursorStack {
  return [...stack, current ?? '']
}

/**
 * Pops the page to return to.
 *
 * An empty stack yields `undefined`, and that is not an error: a pasted deep
 * link legitimately arrives on page forty with no history behind it. What to do
 * about the absence belongs to the caller and not here — the contacts response
 * carries a `prev_cursor` worth falling back to, the inbox's carries nothing and
 * can only go home — so this reports it and recovers nothing on its own.
 */
export function popCursor(stack: CursorStack): { stack: CursorStack; cursor: string | undefined } {
  return { stack: stack.slice(0, -1), cursor: stack[stack.length - 1] || undefined }
}
