import { describe, expect, test } from 'vitest'
import { looksLikeEmail, splitRecipients, mergeRecipients } from '../recipient-parsing'

describe('looksLikeEmail', () => {
  test('accepts an ordinary address', () => {
    expect(looksLikeEmail('ada@prospect.test')).toBe(true)
  })

  test('tolerates surrounding whitespace', () => {
    expect(looksLikeEmail('  ada@prospect.test  ')).toBe(true)
  })

  test('rejects the obviously-wrong cases the field highlights', () => {
    expect(looksLikeEmail('')).toBe(false)
    expect(looksLikeEmail('ada')).toBe(false)
    expect(looksLikeEmail('@prospect.test')).toBe(false)
    expect(looksLikeEmail('ada@')).toBe(false)
    expect(looksLikeEmail('ada@@prospect.test')).toBe(false)
    expect(looksLikeEmail('ada prospect@test')).toBe(false)
  })

  // Deliberately loose: the server does the real RFC 5322 parse, and a stricter
  // client regex would reject valid-but-unusual addresses the server accepts —
  // which is the worse error, since the operator cannot work around it.
  test('accepts valid-but-unusual addresses rather than second-guessing the server', () => {
    expect(looksLikeEmail('ada+campaign@prospect.test')).toBe(true)
    expect(looksLikeEmail("o'brien@prospect.test")).toBe(true)
    expect(looksLikeEmail('ada@localhost')).toBe(true)
  })
})

describe('splitRecipients', () => {
  test('leaves a single in-progress address uncommitted', () => {
    expect(splitRecipients('ada@pros')).toEqual({ committed: [], remainder: 'ada@pros' })
  })

  test('commits on a comma, keeping the tail being typed', () => {
    expect(splitRecipients('ada@x.test, grace@')).toEqual({
      committed: ['ada@x.test'],
      remainder: 'grace@',
    })
  })

  // How a pasted list actually arrives — from another mail client, or a
  // spreadsheet column.
  test('splits on semicolons and whitespace too', () => {
    expect(splitRecipients('a@x.test; b@x.test c@x.test ').committed).toEqual([
      'a@x.test',
      'b@x.test',
      'c@x.test',
    ])
  })

  test('drops empty runs from doubled separators', () => {
    expect(splitRecipients('a@x.test,,  ,b@x.test,').committed).toEqual(['a@x.test', 'b@x.test'])
  })

  test('an empty string commits nothing', () => {
    expect(splitRecipients('')).toEqual({ committed: [], remainder: '' })
  })
})

describe('mergeRecipients', () => {
  test('appends new addresses', () => {
    expect(mergeRecipients(['a@x.test'], ['b@x.test'])).toEqual(['a@x.test', 'b@x.test'])
  })

  // De-duplicating as chips land means the operator sees one chip per person,
  // rather than finding out at send time.
  test('drops a duplicate regardless of case', () => {
    expect(mergeRecipients(['Ada@X.test'], ['ada@x.test'])).toEqual(['Ada@X.test'])
  })

  test('de-duplicates within one paste', () => {
    expect(mergeRecipients([], ['a@x.test', 'A@X.TEST', 'b@x.test'])).toEqual(['a@x.test', 'b@x.test'])
  })

  test('preserves the original casing of what is already there', () => {
    expect(mergeRecipients(['Ada.Lovelace@X.test'], ['ada.lovelace@x.test'])[0]).toBe('Ada.Lovelace@X.test')
  })

  test('an empty addition list changes nothing', () => {
    expect(mergeRecipients(['a@x.test'], [])).toEqual(['a@x.test'])
  })
})
