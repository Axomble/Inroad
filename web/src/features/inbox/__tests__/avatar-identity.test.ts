import { expect, test } from 'vitest'
import { senderInitials, avatarPaletteIndex } from '../avatar-identity'

test('a two-word name takes the first and last initials', () => {
  expect(senderInitials('Jamie Lin')).toBe('JL')
  expect(senderInitials('Ana de la Cruz')).toBe('AC')
})

test('a single word (or an email) takes one initial, not two from one word', () => {
  expect(senderInitials('jamie@prospect.test')).toBe('J')
  expect(senderInitials('Madonna')).toBe('M')
})

test('punctuation-only words are skipped and an empty label degrades to "?"', () => {
  expect(senderInitials('* Jamie *')).toBe('J')
  expect(senderInitials('')).toBe('?')
  expect(senderInitials('   ')).toBe('?')
})

test('non-latin letters count as initials', () => {
  expect(senderInitials('张 伟')).toBe('张伟')
})

test('the palette slot is stable for the same label and always within range', () => {
  const labels = ['Jamie Lin', 'jamie@prospect.test', 'Unknown sender', '张伟', '']
  for (const label of labels) {
    const first = avatarPaletteIndex(label, 6)
    expect(first).toBe(avatarPaletteIndex(label, 6))
    expect(first).toBeGreaterThanOrEqual(0)
    expect(first).toBeLessThan(6)
  }
})

test('a zero-size palette cannot divide by zero', () => {
  expect(avatarPaletteIndex('Jamie Lin', 0)).toBe(0)
})
