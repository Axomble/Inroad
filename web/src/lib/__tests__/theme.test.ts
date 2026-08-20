import { afterEach, beforeEach, expect, test } from 'vitest'
import { PERSIST_STORAGE_KEY } from '@/store/persist-key'
import { applyTheme, readPersistedTheme, resolveDark, startThemeSync } from '../theme'

/** Writes a blob in redux-persist's shape: slice name -> JSON string. */
function writePersisted(slices: Record<string, string>) {
  localStorage.setItem(PERSIST_STORAGE_KEY, JSON.stringify(slices))
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
})

afterEach(() => {
  document.documentElement.classList.remove('dark')
})

test('reads an explicit preference back out of the persisted blob', () => {
  writePersisted({ ui: JSON.stringify({ theme: 'dark' }) })
  expect(readPersistedTheme()).toBe('dark')

  writePersisted({ ui: JSON.stringify({ theme: 'light' }) })
  expect(readPersistedTheme()).toBe('light')
})

test.each([
  ['nothing stored', undefined],
  ['malformed outer json', '{not json'],
  ['outer json without a ui slice', JSON.stringify({ auth: '{}' })],
  ['malformed ui slice', JSON.stringify({ ui: '{not json' })],
  ['ui slice with no theme', JSON.stringify({ ui: '{}' })],
  ['unrecognised theme value', JSON.stringify({ ui: JSON.stringify({ theme: 'chartreuse' }) })],
])('falls back to system for %s', (_label, raw) => {
  if (raw !== undefined) localStorage.setItem(PERSIST_STORAGE_KEY, raw)
  expect(readPersistedTheme()).toBe('system')
})

test('startThemeSync leaves the pre-paint theme alone until the store notifies', () => {
  // main.tsx's real boot order: the pre-paint read applies the persisted theme,
  // then startThemeSync runs in the SAME synchronous tick, while redux-persist
  // has yet to rehydrate and the store still holds initial state. Applying that
  // state on subscribe would undo the pre-paint value and flash the wrong theme.
  applyTheme('dark')
  const listeners: Array<() => void> = []
  const store = {
    getState: () => ({ ui: { theme: 'system' as const } }),
    subscribe: (listener: () => void) => {
      listeners.push(listener)
      return () => listeners.splice(listeners.indexOf(listener), 1)
    },
  }

  const stop = startThemeSync(store)
  expect(document.documentElement).toHaveClass('dark')

  // REHYDRATE (or any later action) makes the store authoritative.
  listeners.forEach((notify) => notify())
  expect(document.documentElement).not.toHaveClass('dark')

  // Teardown really unsubscribes: further notifications must not reapply.
  stop()
  applyTheme('dark')
  listeners.forEach((notify) => notify())
  expect(document.documentElement).toHaveClass('dark')
})

test('resolveDark defers to the system value only for system', () => {
  expect(resolveDark('system', true)).toBe(true)
  expect(resolveDark('system', false)).toBe(false)
  // An explicit choice ignores the OS entirely — the point of storing a tri-state.
  expect(resolveDark('dark', false)).toBe(true)
  expect(resolveDark('light', true)).toBe(false)
})
