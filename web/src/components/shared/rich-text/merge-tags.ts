import type { JSONContent } from '@tiptap/core'

/**
 * The `{{name}}` merge-tag contract between the editor and the send path.
 *
 * Pure, and free of any TipTap runtime import (the `JSONContent` import is
 * type-only), so eager code — form validation — can use it without pulling the
 * editor into the main chunk.
 *
 * The backend stores subject / body_text / body_html as raw templates and
 * substitutes placeholders by literal string match at send time
 * (internal/worker/personalize/personalize.go). The editor therefore never
 * changes a token's spelling: a chip holds the exact text between the braces,
 * including a mistyped or spaced one, and writes it back verbatim.
 */

/**
 * Any `{{...}}` placeholder. Deliberately the same pattern as the backend's
 * validator (`tokenRE` in internal/app/campaign/tokens.go), which has to see
 * the tokens that will NOT be substituted — so does the editor, to flag them.
 */
const TOKEN_PATTERN = /\{\{([^{}]*)\}\}/g

/** A merge field the editor can offer. `name` is the text between the braces. */
export type MergeVariable = { name: string; label: string }

/**
 * - `known`: the send path fills it in.
 * - `unknown`: it would be mailed as typed, or blank — block the save.
 * - `unverified`: can't tell right now (e.g. the custom-field list hasn't
 *   loaded), so it is neither trusted nor blamed.
 */
export type VariableStatus = 'known' | 'unknown' | 'unverified'

export type ClassifyVariable = (name: string) => VariableStatus

export function tokenText(name: string): string {
  return `{{${name}}}`
}

/** Splits one line of template text into text and variable nodes. */
function inlineFromLine(line: string): JSONContent[] {
  const nodes: JSONContent[] = []
  let last = 0
  for (const match of line.matchAll(TOKEN_PATTERN)) {
    if (match.index > last) nodes.push({ type: 'text', text: line.slice(last, match.index) })
    nodes.push({ type: 'variable', attrs: { name: match[1] ?? '' } })
    last = match.index + match[0].length
  }
  if (last < line.length) nodes.push({ type: 'text', text: line.slice(last) })
  return nodes
}

/** One line of text as inline nodes — the subject's whole document. */
export function inlineFromText(text: string): JSONContent[] {
  const nodes: JSONContent[] = []
  text.split('\n').forEach((line, i) => {
    if (i > 0) nodes.push({ type: 'hardBreak' })
    nodes.push(...inlineFromLine(line))
  })
  return nodes
}

/**
 * Plain template text → editor document. A blank line separates paragraphs and
 * a single newline is a hard break, which is the exact inverse of
 * `docToText`: any string survives the trip byte for byte (built as JSON, not
 * parsed as HTML, so whitespace is never collapsed).
 */
export function textToDoc(text: string): JSONContent {
  return {
    type: 'doc',
    content: text.split('\n\n').map((paragraph) => {
      const content = inlineFromText(paragraph)
      return content.length > 0 ? { type: 'paragraph', content } : { type: 'paragraph' }
    }),
  }
}

function inlineToText(nodes: JSONContent[] = []): string {
  return nodes
    .map((node) => {
      if (node.type === 'variable') return tokenText(String(node.attrs?.name ?? ''))
      if (node.type === 'hardBreak') return '\n'
      const text = node.text ?? ''
      const href = node.marks?.find((mark) => mark.type === 'link')?.attrs?.href
      // The text part of a rich email is read by clients that show no links, so
      // an anchor keeps its target — unless the visible text already is it.
      return typeof href === 'string' && href !== text ? `${text} (${href})` : text
    })
    .join('')
}

function listToText(list: JSONContent, depth: number): string {
  const start = typeof list.attrs?.start === 'number' ? list.attrs.start : 1
  const indent = '  '.repeat(depth)
  return (list.content ?? [])
    .map((item, i) => {
      const bullet = list.type === 'orderedList' ? `${start + i}. ` : '- '
      const body = (item.content ?? []).map((block) => blockToText(block, depth + 1)).join('\n')
      return `${indent}${bullet}${body.trimStart()}`
    })
    .join('\n')
}

function blockToText(block: JSONContent, depth = 0): string {
  if (block.type === 'bulletList' || block.type === 'orderedList') return listToText(block, depth)
  return inlineToText(block.content)
}

/**
 * Editor document → the text sent as body_text (and, for a single paragraph,
 * the subject). Formatting marks are dropped; lists become "- " / "1. " lines.
 */
export function docToText(doc: JSONContent): string {
  return (doc.content ?? []).map((block) => blockToText(block)).join('\n\n')
}

const PLAIN_NODES = new Set(['doc', 'paragraph', 'text', 'hardBreak', 'variable'])

/**
 * Whether the document uses any formatting. It decides when a body that began
 * as plain text gains an HTML part: a plain step keeps sending as plain text
 * (no HTML part, no open pixel) until someone actually formats it. A body that
 * already had an HTML part keeps it regardless — see the body editor.
 */
export function isRichDoc(node: JSONContent): boolean {
  if (node.type && !PLAIN_NODES.has(node.type)) return true
  if (node.marks && node.marks.length > 0) return true
  return (node.content ?? []).some(isRichDoc)
}

/**
 * Rewrites stored HTML so every token in a TEXT node becomes the element the
 * variable node parses from. Walks the DOM rather than regex-replacing the
 * string, so a token inside an attribute (`href="…?e={{email}}"`) stays text
 * and the link survives intact.
 */
export function wrapTokensInHtml(html: string): string {
  if (!html.includes('{{')) return html
  const container = document.createElement('div')
  container.innerHTML = html
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT)
  const textNodes: Text[] = []
  for (let node = walker.nextNode(); node; node = walker.nextNode()) textNodes.push(node as Text)
  for (const textNode of textNodes) {
    const value = textNode.data
    if (!value.includes('{{')) continue
    const fragment = document.createDocumentFragment()
    let last = 0
    for (const match of value.matchAll(TOKEN_PATTERN)) {
      if (match.index > last) fragment.append(value.slice(last, match.index))
      const span = document.createElement('span')
      span.setAttribute('data-variable', match[1] ?? '')
      fragment.append(span)
      last = match.index + match[0].length
    }
    if (last === 0) continue
    if (last < value.length) fragment.append(value.slice(last))
    textNode.replaceWith(fragment)
  }
  return container.innerHTML
}

/**
 * The inverse of `wrapTokensInHtml`: the editor's HTML with every variable
 * element replaced by its bare `{{name}}` text — the stored body_html.
 */
export function unwrapTokensInHtml(html: string): string {
  if (!html.includes('data-variable')) return html
  const container = document.createElement('div')
  container.innerHTML = html
  for (const span of container.querySelectorAll('span[data-variable]')) {
    span.replaceWith(tokenText(span.getAttribute('data-variable') ?? ''))
  }
  return container.innerHTML
}

/** Every distinct placeholder name across the templates, sorted. */
export function tokensIn(templates: string[]): string[] {
  const names = new Set<string>()
  for (const template of templates) {
    for (const match of template.matchAll(TOKEN_PATTERN)) names.add(match[1] ?? '')
  }
  return [...names].sort()
}

/** The placeholders the classifier says nothing will fill in. */
export function unknownTokens(templates: string[], classify: ClassifyVariable): string[] {
  return tokensIn(templates).filter((name) => classify(name) === 'unknown')
}
