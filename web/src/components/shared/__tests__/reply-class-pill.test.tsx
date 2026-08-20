import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { ReplyClassPill } from '../reply-class-pill'

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

  // The workspace's own reply-label taxonomy (server-resolved `reply_label`)
  // takes priority over the legacy built-in mapping, since labels can be
  // renamed/recolored per workspace.
  test('a server-resolved label renders its own text and color, not the legacy mapping', () => {
    render(<ReplyClassPill replyClass="positive" replyLabel={{ label: 'Meeting booked', color: '#8b5cf6' }} />)
    expect(screen.getByText('Meeting booked')).toBeInTheDocument()
    expect(screen.queryByText('Positive')).not.toBeInTheDocument()
  })

  test('a null reply_label falls back to the legacy classMeta mapping', () => {
    render(<ReplyClassPill replyClass="positive" replyLabel={null} />)
    expect(screen.getByText('Positive')).toBeInTheDocument()
  })

  test('an unrecognized class with a null label renders nothing', () => {
    const { container } = render(<ReplyClassPill replyClass="bogus" replyLabel={null} />)
    expect(container).toBeEmptyDOMElement()
  })
})
