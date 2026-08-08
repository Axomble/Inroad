/**
 * Money crosses the CRM API as integer *micros* (millionths of a currency
 * unit) so no amount is ever stored as a float. Forms and displays are the only
 * places that speak whole units, so the conversion lives here — one home, one
 * rounding rule — rather than being re-derived at each boundary.
 */

/** Formats integer micros for display in `currency` (ISO 4217, e.g. `USD`). */
export function formatMoney(micros: number, currency: string): string {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumFractionDigits: 0 }).format(
    micros / 1_000_000,
  )
}

/**
 * Formats an already-summed total using the one currency its `items` share.
 * Deals carry their own currency, so a set spanning more than one (or none at
 * all) has no honest single total — those render as an em dash rather than as a
 * number in a currency nobody chose.
 */
export function formatTotal(micros: number, items: readonly { currency: string }[]): string {
  const currencies = new Set(items.map(({ currency }) => currency))
  const [currency] = currencies
  if (currencies.size !== 1 || !currency) return '—'
  return formatMoney(micros, currency)
}

/**
 * Converts a form field holding whole currency units to micros. An empty or
 * non-numeric field means "not set" (`undefined`), which the API distinguishes
 * from a genuine `0`.
 */
export function toMicros(value: string): number | undefined {
  if (value.trim() === '') return undefined
  const amount = Number(value)
  if (!Number.isFinite(amount)) return undefined
  return Math.round(amount * 1_000_000)
}
