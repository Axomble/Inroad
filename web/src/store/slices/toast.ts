import { createSlice, nanoid, type PayloadAction } from '@reduxjs/toolkit'

/**
 * Transient outcome notices for things that finish while you're looking
 * somewhere else.
 *
 * This does NOT replace `NoticeBanner`. A form's own success or failure belongs
 * next to that form, where the eye already is — a toast for it is a notice that
 * moves. Toasts are for outcomes whose origin you may have navigated away from:
 * an import that finished, a campaign that launched, an agent asking for
 * approval. The rule of thumb: if the user is guaranteed to be looking at the
 * thing that caused it, use a banner.
 *
 * Its own slice rather than a member of `ui` because `ui` is the persisted one
 * (`whitelist: ['ui']`). A toast rehydrated from localStorage would announce an
 * import that finished before a reload — stale by construction. Keeping them in
 * separate reducers makes that structural instead of a rule someone remembers.
 */
export type ToastTone = 'ok' | 'error' | 'info'

export interface Toast {
  id: string
  tone: ToastTone
  text: string
  /** Optional deep link to whatever the toast is about. */
  href?: string
  /** Label for `href`. Ignored without one. */
  hrefLabel?: string
}

/** What a caller supplies; the id is minted by the reducer. */
export type ToastInput = Omit<Toast, 'id'>

interface ToastState {
  items: Toast[]
}

const initialState: ToastState = { items: [] }

/**
 * Most toasts on screen at once. Beyond this the oldest is dropped: a stack
 * that grows without limit stops being glanceable and eventually covers the
 * content it's commenting on. Errors are not exempt — an error nobody can read
 * past isn't better than one that scrolled by.
 */
const MAX_VISIBLE = 4

const toastSlice = createSlice({
  name: 'toast',
  initialState,
  reducers: {
    pushToast: {
      reducer: (state, action: PayloadAction<Toast>) => {
        state.items.push(action.payload)
        if (state.items.length > MAX_VISIBLE) {
          state.items.splice(0, state.items.length - MAX_VISIBLE)
        }
      },
      // `prepare` is where the id comes from, so the reducer itself stays a
      // pure function of its input — dispatching the same action twice in a
      // test yields two distinct toasts rather than one deduped by a shared id.
      prepare: (input: ToastInput) => ({ payload: { ...input, id: nanoid() } }),
    },
    dismissToast: (state, action: PayloadAction<string>) => {
      state.items = state.items.filter((toast) => toast.id !== action.payload)
    },
  },
})

export const { pushToast, dismissToast } = toastSlice.actions
export default toastSlice.reducer
