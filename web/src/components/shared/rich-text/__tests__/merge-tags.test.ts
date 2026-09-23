import { describe, expect, test } from 'vitest'
import { docToText, isRichDoc, textToDoc, tokensIn, unknownTokens, wrapTokensInHtml } from '../merge-tags'

// The plain-text path must be byte-exact: body_text and subject are sent as
// typed, so anything the editor adds or drops reaches the prospect.
describe('textToDoc → docToText round-trips exactly', () => {
  test.each([
    ['empty', ''],
    ['plain line', 'Hello there'],
    ['paragraphs and a single line break', 'Hi {{first_name}},\n\nLine one\nLine two'],
    ['adjacent variables', '{{first_name}}{{last_name}}'],
    ['variable at start and end', '{{first_name}} works at {{company}}'],
    ['unknown variable', 'Hi {{firstname}} and {{ first_name }}'],
    ['custom field', 'Your {{custom.industry}} team'],
    ['empty braces', 'odd {{}} token'],
    ['spintax and literal single braces', '{Hi|Hey} {{first_name}}, {a} } {'],
    ['triple braces keep the outer literal ones', '{{{first_name}}}'],
    ['spintax option holding a variable', '{Hi {{first_name}}|Hey}'],
    ['three newlines', 'a\n\n\nb'],
    ['four newlines (an empty paragraph)', 'a\n\n\n\nb'],
    ['leading and trailing breaks', '\n\nstart\n'],
    ['surrounding and doubled spaces', '  two  spaces  '],
  ])('%s', (_name, text) => {
    expect(docToText(textToDoc(text))).toBe(text)
  })
})

test('textToDoc turns tokens into variable nodes and keeps everything else as text', () => {
  expect(textToDoc('Hi {{first_name}}!')).toEqual({
    type: 'doc',
    content: [
      {
        type: 'paragraph',
        content: [
          { type: 'text', text: 'Hi ' },
          { type: 'variable', attrs: { name: 'first_name' } },
          { type: 'text', text: '!' },
        ],
      },
    ],
  })
})

test('docToText derives readable plain text from lists and links', () => {
  const doc = {
    type: 'doc',
    content: [
      {
        type: 'paragraph',
        content: [
          { type: 'text', text: 'See ' },
          { type: 'text', text: 'our site', marks: [{ type: 'link', attrs: { href: 'https://inroad.dev' } }] },
          { type: 'text', text: ' or ' },
          { type: 'text', text: 'https://x.dev', marks: [{ type: 'link', attrs: { href: 'https://x.dev' } }] },
        ],
      },
      {
        type: 'bulletList',
        content: [
          { type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'one' }] }] },
          { type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'variable', attrs: { name: 'company' } }] }] },
        ],
      },
      {
        type: 'orderedList',
        attrs: { start: 3 },
        content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'third' }] }] }],
      },
    ],
  }
  expect(docToText(doc)).toBe('See our site (https://inroad.dev) or https://x.dev\n\n- one\n- {{company}}\n\n3. third')
})

describe('isRichDoc', () => {
  test('plain paragraphs, breaks and variables are not rich', () => {
    expect(isRichDoc(textToDoc('Hi {{first_name}},\n\nthanks\nA'))).toBe(false)
  })
  test('a mark makes it rich', () => {
    expect(
      isRichDoc({
        type: 'doc',
        content: [{ type: 'paragraph', content: [{ type: 'text', text: 'x', marks: [{ type: 'bold' }] }] }],
      }),
    ).toBe(true)
  })
  test('a list makes it rich', () => {
    expect(isRichDoc({ type: 'doc', content: [{ type: 'bulletList', content: [] }] })).toBe(true)
  })
})

describe('wrapTokensInHtml', () => {
  test('wraps tokens in text nodes but never inside attributes', () => {
    const out = wrapTokensInHtml('<p>Hi {{first_name}}, <a href="https://x.dev/?e={{email}}">link</a></p>')
    expect(out).toBe(
      '<p>Hi <span data-variable="first_name"></span>, <a href="https://x.dev/?e={{email}}">link</a></p>',
    )
  })
  test('keeps a spaced token name exactly, so it can be flagged and re-emitted as typed', () => {
    expect(wrapTokensInHtml('<p>{{ first_name }}</p>')).toBe('<p><span data-variable=" first_name "></span></p>')
  })
  test('leaves html without tokens unchanged', () => {
    expect(wrapTokensInHtml('<p>{a|b} &amp; <strong>c</strong></p>')).toBe('<p>{a|b} &amp; <strong>c</strong></p>')
  })
})

test('tokensIn finds every distinct placeholder the backend validator would see', () => {
  expect(tokensIn(['{{b}} {{a}} {{b}}', '{{{c}}}', '{x|y}', '<a href="{{email}}">'])).toEqual(['a', 'b', 'c', 'email'])
})

test('unknownTokens returns only the names the classifier rejects', () => {
  const classify = (name: string) => (name === 'first_name' ? 'known' : name.startsWith('custom.') ? 'unverified' : 'unknown')
  expect(unknownTokens(['{{first_name}} {{firstname}}', '{{custom.x}} {{ first_name }}'], classify)).toEqual([
    ' first_name ',
    'firstname',
  ])
})
