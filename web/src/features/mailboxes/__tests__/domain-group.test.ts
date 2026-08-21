import { describe, expect, test } from 'vitest'
import type { Mailbox, SendingDomain } from '@/store/api'
import { domainGroupLabel, groupMailboxesByDomain, mailboxDomain } from '../domain-group'

const mailbox = (email: string, extra: Partial<Mailbox> = {}): Mailbox => ({
  id: email,
  email,
  status: 'active',
  ...extra,
})

const domain = (name: string, state: SendingDomain['state']): SendingDomain => ({
  domain: name,
  state,
  spf: { found: state === 'passing' },
  dmarc: { found: state === 'passing', policy: 'reject' },
  dkim: { found: false },
  mailbox_count: 1,
  checked_at: state === 'unknown' ? null : new Date().toISOString(),
})

describe('mailboxDomain', () => {
  test('takes everything after the last @, lowercased', () => {
    expect(mailboxDomain('Ines@Axomble.IO')).toBe('axomble.io')
    expect(mailboxDomain('odd"@"address@mail.axomble.io')).toBe('mail.axomble.io')
  })

  test('an address with no @ has no domain rather than throwing', () => {
    expect(mailboxDomain('not-an-address')).toBe('')
    expect(mailboxDomain(undefined)).toBe('')
    expect(domainGroupLabel({ domain: '', mailboxes: [], startIndex: 0 })).toBe('Unknown domain')
  })
})

describe('groupMailboxesByDomain', () => {
  test('every mailbox lands in exactly one group, in the order supplied', () => {
    const mailboxes = [mailbox('a@one.com'), mailbox('b@two.com'), mailbox('c@one.com')]
    const groups = groupMailboxesByDomain(mailboxes, [])

    expect(groups.map((g) => g.domain)).toEqual(['one.com', 'two.com'])
    expect(groups.flatMap((g) => g.mailboxes)).toHaveLength(mailboxes.length)
    expect(groups[0]?.mailboxes.map((m) => m.email)).toEqual(['a@one.com', 'c@one.com'])
  })

  test('start indices are continuous across group boundaries', () => {
    const groups = groupMailboxesByDomain(
      [mailbox('a@one.com'), mailbox('b@two.com'), mailbox('c@one.com')],
      [],
    )
    const indices = groups.flatMap((g) => g.mailboxes.map((_, i) => g.startIndex + i))
    expect(indices).toEqual([0, 1, 2])
  })

  test('a domain needing a DNS record sorts above one merely holding a broken mailbox', () => {
    const groups = groupMailboxesByDomain(
      [mailbox('a@healthy.com', { status: 'error' }), mailbox('b@broken.com')],
      [domain('healthy.com', 'passing'), domain('broken.com', 'failing')],
    )
    expect(groups.map((g) => g.domain)).toEqual(['broken.com', 'healthy.com'])
  })

  test("a domain we couldn't check ranks below a healthy one, never above a failing one", () => {
    const groups = groupMailboxesByDomain(
      [mailbox('a@passing.com'), mailbox('b@unknown.com'), mailbox('c@failing.com')],
      [
        domain('passing.com', 'passing'),
        domain('unknown.com', 'unknown'),
        domain('failing.com', 'failing'),
      ],
    )
    expect(groups.map((g) => g.domain)).toEqual(['failing.com', 'unknown.com', 'passing.com'])
  })

  test('a domain the verdict list does not cover still gets a group, with no verdict', () => {
    const groups = groupMailboxesByDomain([mailbox('new@justconnected.dev')], [])

    expect(groups).toHaveLength(1)
    expect(groups[0]?.auth).toBeUndefined()
    expect(groups[0]?.mailboxes).toHaveLength(1)
  })

  test('verdicts match case-insensitively, so a capitalised address is still authenticated', () => {
    const groups = groupMailboxesByDomain([mailbox('Ines@Axomble.IO')], [domain('axomble.io', 'passing')])
    expect(groups[0]?.auth?.state).toBe('passing')
  })

  test('groups with equal rank are alphabetical, so the order is stable between renders', () => {
    const groups = groupMailboxesByDomain(
      [mailbox('a@zeta.com'), mailbox('b@alpha.com')],
      [domain('zeta.com', 'passing'), domain('alpha.com', 'passing')],
    )
    expect(groups.map((g) => g.domain)).toEqual(['alpha.com', 'zeta.com'])
  })
})
