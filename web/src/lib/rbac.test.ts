import { describe, expect, test } from 'vitest'
import { hasMinRole, WORKSPACE_ROLES } from './rbac'

describe('hasMinRole', () => {
  test('a role satisfies its own minimum and every one below it', () => {
    expect(hasMinRole('owner', 'owner')).toBe(true)
    expect(hasMinRole('owner', 'admin')).toBe(true)
    expect(hasMinRole('owner', 'member')).toBe(true)

    expect(hasMinRole('admin', 'admin')).toBe(true)
    expect(hasMinRole('admin', 'member')).toBe(true)

    expect(hasMinRole('member', 'member')).toBe(true)
  })

  test('a role never satisfies a minimum above it', () => {
    expect(hasMinRole('member', 'admin')).toBe(false)
    expect(hasMinRole('member', 'owner')).toBe(false)
    expect(hasMinRole('admin', 'owner')).toBe(false)
  })

  // The server ranks an unrecognized role 0, so it clears no minimum. The
  // client must agree: guessing upward for a role from a newer server would
  // render controls whose requests then 403.
  test('fails closed on absent, empty and unrecognized roles', () => {
    for (const unknown of [null, undefined, '', 'Admin', 'ADMIN', 'superuser', 'guest']) {
      expect(hasMinRole(unknown, 'member')).toBe(false)
      expect(hasMinRole(unknown, 'admin')).toBe(false)
      expect(hasMinRole(unknown, 'owner')).toBe(false)
    }
  })

  // Guards the ordering itself: the table is authority-ascending, and the rank
  // derivation depends on that. A reordered array would invert every gate.
  test('WORKSPACE_ROLES is ordered lowest authority first', () => {
    expect(WORKSPACE_ROLES).toEqual(['member', 'admin', 'owner'])
    for (const [index, role] of WORKSPACE_ROLES.entries()) {
      // Every role clears every minimum at or below its position, and none above.
      for (const [otherIndex, other] of WORKSPACE_ROLES.entries()) {
        expect(hasMinRole(role, other)).toBe(index >= otherIndex)
      }
    }
  })
})
