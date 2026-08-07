import { httpStatus } from '@/lib/rtk-error'

type LabelAction = 'create' | 'update' | 'delete' | 'reorder'

/**
 * One home for mapping the reply-label API's failure statuses to user copy,
 * so the dialog, the delete confirm, and the reorder list don't each grow
 * their own ad-hoc status switch.
 */
export function replyLabelErrorMessage(action: LabelAction, error: unknown): string {
  const status = httpStatus(error)
  switch (action) {
    case 'create':
      if (status === 409) return 'A label with that name already exists.'
      if (status === 422) return 'The server rejected this label — check the name, color, and flags.'
      return "Couldn't create the label. Please try again."
    case 'update':
      if (status === 404) return 'That label no longer exists — it may have been deleted.'
      if (status === 422) return 'The server rejected this change — check the name, color, and flags.'
      return "Couldn't save the label. Please try again."
    case 'delete':
      if (status === 404) return 'That label was already deleted.'
      if (status === 409) return 'Built-in labels cannot be deleted — rename or recolor them instead.'
      return "Couldn't delete the label. Please try again."
    case 'reorder':
      if (status === 422) return 'The list changed while you were reordering — it has been refreshed, try again.'
      return "Couldn't reorder the labels. Please try again."
  }
}
