import { z } from 'zod'

/**
 * Client-side warmup settings validation, mirroring the API contract's
 * boundary rules (openapi `WarmupSettings`):
 *   1 ≤ start_volume ≤ max_volume ≤ 200, ramp_increment ≥ 1, 0 ≤ reply_rate ≤ 1.
 * Kept in its own module (not inline in the form) so the rules are unit-tested
 * directly and reused without dragging in the component. The form registers
 * fields with `valueAsNumber`, so values arrive as numbers (an empty field is
 * NaN, which fails the range checks) — no string coercion needed, and the
 * schema's input and output types match (a clean resolver).
 */
/**
 * Is this an IANA zone this runtime knows? Intl throws RangeError on an unknown
 * zone, which is the only reliable in-browser check — and it is the same
 * question the server answers with time.LoadLocation.
 */
function isKnownTimeZone(zone: string): boolean {
  try {
    // Called, not constructed: the probe is the RangeError an unknown zone
    // throws, and `new` purely for a side effect is a lint error here.
    Intl.DateTimeFormat('en-US', { timeZone: zone })
    return true
  } catch {
    return false
  }
}

export const warmupSettingsSchema = z
  .object({
    start_volume: z
      .number({ message: 'Enter a number' })
      .int('Whole numbers only')
      .min(1, 'At least 1')
      .max(200, 'At most 200'),
    max_volume: z
      .number({ message: 'Enter a number' })
      .int('Whole numbers only')
      .min(1, 'At least 1')
      .max(200, 'At most 200'),
    ramp_increment: z
      .number({ message: 'Enter a number' })
      .int('Whole numbers only')
      .min(1, 'At least 1'),
    reply_rate: z
      .number({ message: 'Enter a number' })
      .min(0, 'At least 0')
      .max(1, 'At most 1'),
    // The zone the mailbox's waking-hours window is read in. Checked against the
    // runtime's own IANA database rather than a hardcoded list, so the rule
    // cannot drift from the server's time.LoadLocation as zones are renamed.
    timezone: z
      .string()
      .min(1, 'Pick a timezone')
      .refine(isKnownTimeZone, 'Not a known timezone'),
  })
  .refine((v) => v.start_volume <= v.max_volume, {
    message: "Start volume can't exceed max volume",
    path: ['start_volume'],
  })

export type WarmupSettingsValues = z.infer<typeof warmupSettingsSchema>

/** Sensible first-run defaults, matching the backend column defaults. */
export const warmupSettingsDefaults: WarmupSettingsValues = {
  start_volume: 4,
  max_volume: 40,
  ramp_increment: 2,
  reply_rate: 0.3,
  // The browser's own zone is the right default: warmup is meant to look like a
  // person sending, and the person setting it up is almost always in the zone
  // that person works in. Falls back to UTC, which is the column default.
  timezone: browserTimeZone(),
}

/** The viewer's IANA zone, or UTC when the runtime will not say. */
export function browserTimeZone(): string {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  return zone && isKnownTimeZone(zone) ? zone : 'UTC'
}
