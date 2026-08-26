// Pure helpers behind SenderAvatar — initials and a stable palette slot.
// Component-free so the rules are unit-tested directly, and so the avatar
// component file exports only a component (fast refresh), matching the
// contact-label.ts split.

/**
 * The one-or-two letter monogram a mail client stamps on a sender's avatar.
 *
 * Word-initials first ("Jamie Lin" → "JL"), because a name is the label the
 * operator recognises. An email address takes its first letter only — "jl"
 * from "jamie@…" would claim two words where there is one. Anything empty or
 * non-alphanumeric degrades to "?" rather than an empty circle.
 */
export function senderInitials(label: string): string {
  const words = label
    .split(/\s+/)
    .map((w) => firstAlphanumeric(w))
    .filter((c): c is string => c !== undefined)
  if (words.length >= 2) return `${words[0]}${words[words.length - 1]}`.toUpperCase()
  if (words.length === 1) return words[0]!.toUpperCase()
  return '?'
}

function firstAlphanumeric(word: string): string | undefined {
  const match = /[\p{L}\p{N}]/u.exec(word)
  return match?.[0]
}

/**
 * Which of `size` palette slots a sender gets — a stable hash of the label, so
 * the same contact wears the same colour on every row, every visit, without
 * anything persisted. FNV-1a, because it is tiny and spreads short ASCII
 * strings (names, emails) evenly, which is the whole job.
 */
export function avatarPaletteIndex(label: string, size: number): number {
  if (size <= 0) return 0
  let hash = 0x811c9dc5
  for (let i = 0; i < label.length; i++) {
    hash ^= label.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return Math.abs(hash) % size
}
