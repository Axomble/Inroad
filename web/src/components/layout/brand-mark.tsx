/**
 * The Inroad mark: a folded paper plane, climbing.
 *
 * Deliberately NOT a plane inside a filled circle — that silhouette is
 * Telegram's, and on an email product it reads as "messaging app" and gets
 * misattributed on sight. This is the plane alone, which also survives being
 * shrunk to a 16px favicon far better than a disc with a shape knocked out of
 * it.
 *
 * Drawn as three faces rather than one outline, because that is what makes it
 * read as FOLDED PAPER instead of a generic arrow:
 *
 *   · wing   — the near, lit upper surface (full strength)
 *   · body   — the far wing beyond the fold (mid)
 *   · shadow — the underside of the near wing (faint)
 *
 * The fold is the boundary between the faces, so no stroke is needed and the
 * shape stays crisp at every size.
 *
 * Colour comes from `currentColor` alone, so ONE file serves both themes: the
 * caller sets the text colour and the three faces derive from it via opacity.
 * That is why there is no lime here and no second asset to keep in sync.
 * Keep this geometry identical to web/public/brand/inroad-mark.svg (the
 * favicon), which is this component flattened to static fills.
 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 48 48"
      className={className}
      fill="currentColor"
      role="img"
      aria-label="Inroad"
    >
      {/* far wing, beyond the fold */}
      <path d="M44 6 26 43 21 27Z" opacity="0.45" />
      {/* underside of the near wing */}
      <path d="M21 27 44 6 26 43Z" opacity="0.7" />
      {/* near wing: the surface the eye reads first */}
      <path d="M4 21 44 6 21 27Z" />
    </svg>
  )
}
