/**
 * One run of text and whether it matched the query. Declared here rather than
 * imported from a feature's search type so this component stays free of any
 * domain — any API that splits text into matched and unmatched runs (the
 * inbox search's highlight segments are one) is structurally assignable.
 */
export interface HighlightSegment {
  text: string
  match: boolean
}

/**
 * Renders pre-split search-highlight segments, wrapping the matched runs in
 * `<mark>`.
 *
 * Every run is a React text node, so the text is escaped by construction:
 * highlight segments carry message content, which can contain anything —
 * markup included — and must never be interpreted as HTML. A `<mark>` is the
 * element screen readers can announce as highlighted, and the tint is from the
 * theme's warn token so it reads in light and dark alike (the UA's default
 * yellow-on-black does not).
 */
export function HighlightedText({
  segments,
  className,
}: {
  segments: readonly HighlightSegment[]
  className?: string
}) {
  return (
    <span className={className}>
      {segments.map((segment, i) => {
        const Run = segment.match ? 'mark' : 'span'
        return (
          // oxlint-disable-next-line no-array-index-key -- the segments are a fixed, ordered split of one string, only ever replaced wholesale, and two runs may carry identical text, so position is the identity
          <Run key={i} className={segment.match ? 'rounded-[2px] bg-warn/25 font-semibold text-foreground' : undefined}>
            {segment.text}
          </Run>
        )
      })}
    </span>
  )
}
