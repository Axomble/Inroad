import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { expect, test } from 'vitest'

// `components/shared` is where UI goes when more than one feature needs it. That
// only works if it depends on no feature at all: the moment a shared component
// reaches into `@/features/x`, every feature using it inherits that dependency,
// which is how the contacts screens ended up importing CRM UI in the first place.
//
// A convention in CLAUDE.md is a request; this is the part that actually holds.
// Scoped to this directory deliberately — `components/layout` legitimately reads
// the pulse feature's types, and several features import each other today. Those
// are separate, pre-existing questions, and widening this test would turn it into
// a list of known failures instead of a guard.

const here = dirname(fileURLToPath(import.meta.url))

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    if (!/\.tsx?$/.test(entry.name) || entry.name.includes('.test.')) return []
    return [path]
  })
}

test('no shared component depends on a feature', () => {
  const offenders = sourceFiles(here)
    .map((path) => ({ path, source: readFileSync(path, 'utf8') }))
    .filter(({ source }) => /from '@\/features\//.test(source))
    .map(({ path }) => path.slice(here.length + 1))

  expect(offenders).toEqual([])
})

test('the record-page shell is one of the files that guard covers', () => {
  // Guards silently pass when their input goes missing, so this pins the file the
  // guard exists for — a rename that moved the shell out of scope would otherwise
  // leave a green test asserting nothing.
  expect(sourceFiles(here).map((path) => path.slice(here.length + 1))).toContain('record-page.tsx')
})
