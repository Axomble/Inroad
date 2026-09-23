/**
 * Whether stored HTML survives the body editor's schema.
 *
 * The editor keeps paragraphs, line breaks, bold, italic, links and lists —
 * nothing else. Loading anything richer and saving would silently drop it, so
 * the body editor checks first and keeps such HTML out of the editor.
 *
 * An allowlist, not a parse-and-compare: it can name what would be lost (the
 * notice needs that), and it errs the safe way — a false positive only means
 * the HTML is shown raw. `html-compat.test.ts` and the body editor's round-trip
 * tests keep the list equal to what the schema really preserves.
 *
 * Pure DOM, no TipTap import.
 */

/** Elements the schema parses without loss (`b`/`i` are alternate spellings of bold/italic). */
const KEPT_TAGS = new Set(['p', 'br', 'strong', 'b', 'em', 'i', 'a', 'ul', 'ol', 'li', 'span'])

/** Attributes the schema stores per element — the Link and OrderedList extensions' own. */
const KEPT_ATTRIBUTES: Record<string, ReadonlySet<string>> = {
  a: new Set(['href', 'target', 'rel', 'class', 'title']),
  ol: new Set(['start', 'type']),
  span: new Set(['data-variable']),
}

/** What a person calls each dropped element, so the notice reads as formatting rather than markup. */
const TAG_LABELS: Record<string, string> = {
  h1: 'headings',
  h2: 'headings',
  h3: 'headings',
  h4: 'headings',
  h5: 'headings',
  h6: 'headings',
  table: 'tables',
  thead: 'tables',
  tbody: 'tables',
  tfoot: 'tables',
  tr: 'tables',
  td: 'tables',
  th: 'tables',
  caption: 'tables',
  colgroup: 'tables',
  col: 'tables',
  img: 'images',
  picture: 'images',
  div: 'layout blocks',
  section: 'layout blocks',
  center: 'inline styles',
  font: 'inline styles',
  style: 'style sheets',
  blockquote: 'quotes',
  pre: 'code blocks',
  code: 'code blocks',
  hr: 'dividers',
  u: 'underline',
  s: 'strikethrough',
  strike: 'strikethrough',
  del: 'strikethrough',
}

function attributeLabel(name: string): string {
  if (name === 'style') return 'inline styles'
  if (name === 'class') return 'CSS classes'
  return `${name} attributes`
}

/**
 * The formatting the editor would drop from this HTML, as readable labels,
 * sorted and de-duplicated. Empty means it can be edited without loss.
 */
export function unkeptFormatting(html: string): string[] {
  if (html.trim() === '') return []
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const lost = new Set<string>()

  // The whole document, not just <body>: a leading comment sits before <html>,
  // and a <style>, <meta> or <title> before the content lands in <head>.
  const walker = doc.createTreeWalker(doc, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_COMMENT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.COMMENT_NODE) {
      // Includes Outlook's <!--[if mso]> conditionals.
      lost.add('HTML comments')
      continue
    }
    const element = node as Element
    const tag = element.localName
    if (tag === 'html' || tag === 'head' || tag === 'body') {
      // The wrappers themselves are dropped harmlessly; what they carry is not.
      if (element.attributes.length > 0) lost.add('page styles')
      continue
    }
    if (element.parentElement === doc.head) {
      lost.add(TAG_LABELS[tag] ?? 'page styles')
      continue
    }
    const keptTag = KEPT_TAGS.has(tag)
    if (!keptTag) lost.add(TAG_LABELS[tag] ?? `<${tag}> elements`)
    const kept = KEPT_ATTRIBUTES[tag]
    for (const attribute of element.attributes) {
      if (kept?.has(attribute.name)) continue
      // A dropped element's own attributes (an image's src) go with it; only
      // styling on it is worth naming separately.
      if (!keptTag && attribute.name !== 'style' && attribute.name !== 'class') continue
      lost.add(attributeLabel(attribute.name))
    }
  }
  return [...lost].sort()
}
