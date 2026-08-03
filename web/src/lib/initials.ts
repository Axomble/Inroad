/**
 * Initials for an identity avatar, or `null` when there is no identity to
 * derive them from.
 *
 * `null` rather than a `'?'` placeholder: on a hard reload the session is
 * restored asynchronously, so name and email are both briefly absent. Rendering
 * `?` in that window flashed a wrong-looking identity on every refresh — the
 * caller renders a neutral skeleton instead ("loading", not "we don't know who
 * you are").
 *
 * Shared by the header account menu and the sidebar identity footer so the two
 * avatars can never disagree about the same person.
 */
export function initialsFromIdentity(name: string | null, email: string | null): string | null {
  const source = name?.trim() || email?.trim() || ''
  if (!source) return null
  const parts = source.split(/[\s@._-]+/).filter(Boolean)
  const first = parts[0]?.[0] ?? ''
  const second = parts[1]?.[0] ?? ''
  const letters = (first + second).toUpperCase()
  return letters || source[0]!.toUpperCase()
}
