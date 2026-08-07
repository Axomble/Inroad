import { fireEvent, render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'
import { GatedActionButton } from './gated-action-button'

// The shared "the server would refuse this right now" control. What matters is
// that a blocked action is refused AND explains itself to a keyboard/screen
// reader user — a plain `disabled` button with a hover tooltip does neither.

const REASON = 'Verify your email address to connect a mailbox.'

test('an ungated button behaves like a plain Button', () => {
  const onClick = vi.fn()
  render(
    <GatedActionButton blocked={false} reason={REASON} onClick={onClick}>
      Connect mailbox
    </GatedActionButton>,
  )

  const button = screen.getByRole('button', { name: 'Connect mailbox' })
  expect(button).not.toHaveAttribute('aria-disabled')
  expect(button).not.toHaveAttribute('aria-describedby')
  fireEvent.click(button)
  expect(onClick).toHaveBeenCalledTimes(1)
})

test('a gated button refuses its click handler', () => {
  const onClick = vi.fn()
  render(
    <GatedActionButton blocked reason={REASON} onClick={onClick}>
      Connect mailbox
    </GatedActionButton>,
  )

  fireEvent.click(screen.getByRole('button', { name: 'Connect mailbox' }))
  expect(onClick).not.toHaveBeenCalled()
})

test('a gated button stays focusable and is programmatically described by the reason', () => {
  render(
    <GatedActionButton blocked reason={REASON}>
      Connect mailbox
    </GatedActionButton>,
  )

  const button = screen.getByRole('button', { name: 'Connect mailbox' })
  // aria-disabled, not `disabled`: the reason has to be reachable by keyboard.
  expect(button).toHaveAttribute('aria-disabled', 'true')
  expect(button).not.toBeDisabled()
  button.focus()
  expect(button).toHaveFocus()

  const hintId = button.getAttribute('aria-describedby')
  expect(hintId).toBeTruthy()
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(REASON)
})

test("a gated button drops the caller's own disabled so the explanation stays reachable", () => {
  render(
    <GatedActionButton blocked reason={REASON} disabled>
      Connect mailbox
    </GatedActionButton>,
  )

  expect(screen.getByRole('button', { name: 'Connect mailbox' })).not.toBeDisabled()
})

test('a blocked submit button does not submit its form', () => {
  const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault())
  render(
    <form onSubmit={onSubmit}>
      <GatedActionButton blocked reason={REASON} type="submit">
        Connect mailbox
      </GatedActionButton>
    </form>,
  )

  fireEvent.click(screen.getByRole('button', { name: 'Connect mailbox' }))
  expect(onSubmit).not.toHaveBeenCalled()
})
