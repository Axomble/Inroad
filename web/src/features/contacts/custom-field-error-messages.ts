import { httpStatus, serverDetail } from '@/lib/rtk-error'

type FieldAction = 'create' | 'update' | 'archive' | 'saveValues'

/**
 * One home for mapping the custom-field API's failure statuses to user copy, so
 * the settings dialog, the archive confirm and the contact value form don't each
 * grow their own status switch.
 *
 * 400 is special here and deliberately prefers the server's own sentence. This
 * API's validation errors name the offending field and the exact reason
 * ("renewal: value must be a date in YYYY-MM-DD form"), which is strictly more
 * useful than anything this module could say without duplicating the backend's
 * type rules — and duplicating them is how the two drift apart.
 */
export function customFieldErrorMessage(action: FieldAction, error: unknown): string {
  const status = httpStatus(error)
  const detail = serverDetail(error)
  if (status === 400 && detail) return detail

  switch (action) {
    case 'create':
      if (status === 409) {
        return (
          detail ??
          'That key is already used. Keys are kept even after a field is archived, so pick a different one.'
        )
      }
      return "Couldn't create the field. Please try again."
    case 'update':
      if (status === 404) return 'That field no longer exists — it may have been archived by someone else.'
      if (status === 409) return 'That field is archived, so it can no longer be edited.'
      return "Couldn't save the field. Please try again."
    case 'archive':
      if (status === 404) return 'That field no longer exists.'
      return "Couldn't archive the field. Please try again."
    case 'saveValues':
      if (status === 404) return 'This contact no longer exists.'
      return "Couldn't save these fields. Please try again."
  }
}
