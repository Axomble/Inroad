// Parsing rules for the composer's recipient fields. Component-free so the
// rules are unit-testable directly and the component file stays fast-refresh
// friendly — the same split inbox-search.ts and thread-buckets.ts already use.

/**
 * Whether a string could plausibly be an email address.
 *
 * Deliberately loose — one `@` with something either side and no whitespace.
 * The server does the real RFC 5322 parse; this only exists so the field can
 * mark an obviously-wrong chip rather than accepting it silently and failing at
 * send time. A stricter client regex would reject valid-but-unusual addresses
 * the server would have taken, which is the worse error.
 */
export function looksLikeEmail(value: string): boolean {
  const trimmed = value.trim()
  if (/\s/.test(trimmed)) return false
  const at = trimmed.indexOf('@')
  return at > 0 && at < trimmed.length - 1 && trimmed.lastIndexOf('@') === at
}

/**
 * Splits typed text into finished addresses plus whatever is still being typed.
 *
 * Commas, semicolons and whitespace all commit a chip, because that is how
 * addresses arrive when pasted out of another mail client or a spreadsheet — a
 * field that only accepted Enter would make pasting a list unusable.
 */
export function splitRecipients(raw: string): { committed: string[]; remainder: string } {
  const parts = raw.split(/[,;\s]+/)
  const remainder = parts.pop() ?? ''
  return { committed: parts.filter((p) => p !== ''), remainder }
}

/**
 * Adds newly-typed addresses to an existing list, dropping any that are already
 * there (compared case-insensitively).
 *
 * De-duplicating as chips LAND rather than at send time means the operator sees
 * one chip per person and never discovers the duplicate in a rejection.
 */
export function mergeRecipients(existing: readonly string[], additions: readonly string[]): string[] {
  const seen = new Set(existing.map((v) => v.toLowerCase()))
  const kept: string[] = []
  for (const candidate of additions) {
    const lowered = candidate.toLowerCase()
    if (seen.has(lowered)) continue
    seen.add(lowered)
    kept.push(candidate)
  }
  return kept.length === 0 ? [...existing] : [...existing, ...kept]
}
