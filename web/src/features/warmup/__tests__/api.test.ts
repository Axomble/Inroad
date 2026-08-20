import { expect, expectTypeOf, test } from 'vitest'
import { api } from '@/store/api'
import type {
  WarmupOverview,
  WarmupDetail,
  WarmupMailbox,
  WarmupParticipant,
  WarmupSettings,
  GetMailboxWarmupApiArg,
} from '@/store/api'
import {
  useGetWarmupOverviewQuery,
  useGetMailboxWarmupQuery,
  useEnableMailboxWarmupMutation,
  useDisableMailboxWarmupMutation,
} from '../api'

// A light contract guard: the warmup endpoints the UI binds to must stay wired
// on the generated api, and their generated types must keep the shapes the
// components read. If gen:api reshapes the contract these break at build/test
// time rather than silently at runtime.

test('the enhanced warmup endpoints are registered and expose hooks', () => {
  for (const name of [
    'getWarmupOverview',
    'getMailboxWarmup',
    'enableMailboxWarmup',
    'disableMailboxWarmup',
  ] as const) {
    expect(api.endpoints[name]).toBeDefined()
  }

  expect(useGetWarmupOverviewQuery).toBeTypeOf('function')
  expect(useGetMailboxWarmupQuery).toBeTypeOf('function')
  expect(useEnableMailboxWarmupMutation).toBeTypeOf('function')
  expect(useDisableMailboxWarmupMutation).toBeTypeOf('function')
})

test('the generated warmup types keep the fields the UI depends on', () => {
  expectTypeOf<WarmupOverview>().toMatchObjectType<{
    pool_size: number
    active: boolean
  }>()
  // The sparkline reads sent/received off each series entry.
  expectTypeOf<WarmupDetail['series'][number]>().toMatchObjectType<{
    sent: number
    received: number
  }>()
  expectTypeOf<WarmupParticipant['health_state']>().toEqualTypeOf<
    'unknown' | 'healthy' | 'watch' | 'throttled' | 'paused'
  >()
  // Reputation and pool eligibility are separate axes with separate
  // vocabularies; the lane enum must stay whole (a dropped value would silently
  // fall back to `probation` at runtime) and must not collapse into health's.
  expectTypeOf<WarmupParticipant['lane']>().toEqualTypeOf<
    'pending_auth' | 'probation' | 'healthy' | 'watch' | 'recovery' | 'quarantine' | 'blocked'
  >()
  expectTypeOf<WarmupMailbox>().toMatchObjectType<{ lane_reason: string }>()
  expectTypeOf<GetMailboxWarmupApiArg>().toMatchObjectType<{ id: string }>()
  expectTypeOf<WarmupSettings>().toMatchObjectType<{ start_volume?: number }>()
})
