import { useMemo } from 'react'
import { useAppDispatch } from '@/store/hooks'
import { pushToast, type ToastInput } from '@/store/slices/toast'

/**
 * Raise a transient outcome notice. See `store/slices/toast.ts` for when a
 * toast is the right surface and when a `NoticeBanner` is.
 *
 * The returned object is memoized on the dispatch identity so it's safe in an
 * effect's dependency list — a fresh object each render would re-run every
 * effect that depends on it.
 */
export function useToast() {
  const dispatch = useAppDispatch()
  return useMemo(
    () => ({
      ok: (text: string, options?: Omit<ToastInput, 'tone' | 'text'>) =>
        dispatch(pushToast({ tone: 'ok', text, ...options })),
      error: (text: string, options?: Omit<ToastInput, 'tone' | 'text'>) =>
        dispatch(pushToast({ tone: 'error', text, ...options })),
      info: (text: string, options?: Omit<ToastInput, 'tone' | 'text'>) =>
        dispatch(pushToast({ tone: 'info', text, ...options })),
    }),
    [dispatch],
  )
}
