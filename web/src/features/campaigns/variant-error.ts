import { httpStatus, serverDetail } from '@/lib/rtk-error'

type VariantAction = 'create' | 'update' | 'delete' | 'baseWeight'

/**
 * One home for mapping the A/B variant API's failure statuses to user copy.
 *
 * 409 prefers the server's own sentence, because this API has four distinct
 * conflicts — label taken, variant limit reached, the variant has already sent,
 * and "nothing would be left able to send" — and which one you hit is the entire
 * content of the message. Restating them here would duplicate rules that live in
 * the service, which is how the two drift apart.
 */
export function variantErrorMessage(action: VariantAction, error: unknown): string {
  const status = httpStatus(error)
  const detail = serverDetail(error)
  if ((status === 409 || status === 400) && detail) return detail

  switch (action) {
    case 'create':
      if (status === 404) return 'That step no longer exists — it may have been deleted.'
      return "Couldn't add the variant. Please try again."
    case 'update':
      if (status === 404) return 'That variant no longer exists.'
      return "Couldn't save the variant. Please try again."
    case 'delete':
      if (status === 404) return 'That variant was already deleted.'
      if (status === 409) {
        return 'This variant has already sent. Set its weight to 0 instead — deleting it would fold its results into the others.'
      }
      return "Couldn't delete the variant. Please try again."
    case 'baseWeight':
      if (status === 404) return 'That step no longer exists.'
      if (status === 409) return 'At least one variant has to keep a weight above zero, or the step can’t send.'
      return "Couldn't change the split. Please try again."
  }
}

/**
 * The share of sends each arm receives, as whole percentages that sum to 100.
 *
 * Weights are relative, not percentages, so the editor has to derive the split
 * to show it — an operator setting 3 and 1 wants to see 75% / 25%, not "3" and
 * "1". Zero-weight arms are excluded from the denominator because they are
 * retired and receive nothing.
 *
 * Returns an empty map when nothing is eligible, which the caller renders as the
 * error state it is (that step cannot send at all).
 */
export function splitShares(weights: Record<string, number>): Record<string, number> {
  const total = Object.values(weights).reduce((sum, w) => sum + Math.max(0, w), 0)
  if (total === 0) return {}
  const shares: Record<string, number> = {}
  for (const [key, weight] of Object.entries(weights)) {
    shares[key] = weight > 0 ? Math.round((weight / total) * 100) : 0
  }
  return shares
}
