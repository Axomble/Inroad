import { cn } from '@/lib/utils'
import { senderInitials, avatarPaletteIndex } from './avatar-identity'

/**
 * The initials circle a mail client puts on every row and message — the
 * fastest way to tell senders apart while scanning, faster than reading names.
 *
 * Colours are theme tokens only (never arbitrary hex): each combo is a tinted
 * background of a semantic colour with that colour as the monogram, so both
 * themes keep contrast without per-theme cases here. The slot is a stable hash
 * of the label — see avatarPaletteIndex.
 */
const AVATAR_PALETTE = [
  'bg-data/15 text-data',
  'bg-security/15 text-security',
  'bg-warm/15 text-warm',
  'bg-ok/15 text-ok',
  'bg-accent-ink/15 text-accent-ink',
  'bg-danger/15 text-danger',
] as const

export function SenderAvatar({
  label,
  size = 'md',
  className,
}: {
  /** The sender's display label (name or email) — both the monogram and the colour derive from it. */
  label: string
  size?: 'sm' | 'md'
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'flex shrink-0 select-none items-center justify-center rounded-full font-semibold',
        size === 'md' ? 'size-8 text-[11px]' : 'size-7 text-[10px]',
        AVATAR_PALETTE[avatarPaletteIndex(label, AVATAR_PALETTE.length)],
        className,
      )}
    >
      {senderInitials(label)}
    </span>
  )
}
