import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { ReplyClassPill } from './reply-class-pill'

describe('ReplyClassPill', () => {
  // The text label is the primary signal (color is redundant, never sole) —
  // assert on the human label for each of the seven classes.
  test.each([
    ['positive', 'Positive'],
    ['negative', 'Negative'],
    ['neutral', 'Neutral'],
    ['out_of_office', 'Out of office'],
    ['auto_reply', 'Auto-reply'],
    ['unsubscribe', 'Unsubscribed'],
    ['unknown', 'Unknown'],
  ] as const)('renders the %s class as "%s"', (replyClass, label) => {
    render(<ReplyClassPill replyClass={replyClass} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })

  test('renders nothing when reply class is null', () => {
    const { container } = render(<ReplyClassPill replyClass={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('renders nothing when reply class is undefined', () => {
    const { container } = render(<ReplyClassPill replyClass={undefined} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('renders nothing for an unrecognized class string', () => {
    const { container } = render(<ReplyClassPill replyClass={'bogus'} />)
    expect(container).toBeEmptyDOMElement()
  })
})
