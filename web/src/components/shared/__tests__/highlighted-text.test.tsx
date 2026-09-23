import { render } from '@testing-library/react'
import { expect, test } from 'vitest'
import { HighlightedText } from '../highlighted-text'

test('wraps only the matched runs in <mark>, in order', () => {
  const { container } = render(
    <HighlightedText
      segments={[
        { text: 'Can we book a ', match: false },
        { text: 'meeting', match: true },
        { text: ' next ', match: false },
        { text: 'week', match: true },
      ]}
    />,
  )

  expect(container.textContent).toBe('Can we book a meeting next week')
  const marks = [...container.querySelectorAll('mark')].map((m) => m.textContent)
  expect(marks).toEqual(['meeting', 'week'])
})

test('renders markup in a segment as literal text, never as HTML', () => {
  const { container } = render(
    <HighlightedText
      segments={[
        { text: '<img src=x onerror="alert(1)">', match: false },
        { text: '<b>pricing</b>', match: true },
      ]}
    />,
  )

  // No element was created from the segment text — only our own mark.
  expect(container.querySelector('img')).toBeNull()
  expect(container.querySelector('b')).toBeNull()
  expect(container.querySelector('mark')?.textContent).toBe('<b>pricing</b>')
  expect(container.textContent).toBe('<img src=x onerror="alert(1)"><b>pricing</b>')
})

test('renders no marks when nothing matched, and nothing at all for no segments', () => {
  const { container, rerender } = render(<HighlightedText segments={[{ text: 'plain body', match: false }]} />)
  expect(container.querySelector('mark')).toBeNull()
  expect(container.textContent).toBe('plain body')

  rerender(<HighlightedText segments={[]} />)
  expect(container.textContent).toBe('')
})
