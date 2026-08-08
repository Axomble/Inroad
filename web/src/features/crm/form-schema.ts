// Field rules shared by the CRM create forms. They live apart from the forms
// themselves so the Companies page and the Deals page validate a currency or an
// amount the same way without either importing the other's component module.
import { z } from 'zod'

/**
 * The API rejects anything but three letters (`^[A-Z]{3}$`) and the forms
 * upper-case before sending, so the client mirrors that rule rather than merely
 * counting characters — `12a` used to pass here and 400 at the server.
 */
export const currencyField = z
  .string()
  .trim()
  .regex(/^[A-Za-z]{3}$/, 'Use a three-letter currency code')

/** An optional money field in whole currency units; `toMicros` converts it. */
export const optionalMoney = z
  .string()
  .refine((value) => value === '' || (Number.isFinite(Number(value)) && Number(value) >= 0), 'Enter a positive amount')
