// The fleet operator view's data seam.
//
// There is deliberately NO `enhanceEndpoints` call and no cache tag. Tags exist
// to let a mutation invalidate what it changed, and this feature has no
// mutations at all — nothing on these screens writes. A tag nothing invalidates
// is a cache dependency no one can observe, which is the reason the dead-letter
// feature's tags are scoped the way they are and the reason there are none here.
//
// The file exists anyway, rather than importing `@/store/api` from each
// component, so that this feature has exactly one place where its server
// contract enters — the same shape every other feature has.
export {
  useListFleetWorkersQuery,
  useListFleetDecisionsQuery,
  useListScheduledJobsQuery,
} from '@/store/api'

export type {
  FleetWorker,
  FleetProviderSignals,
  FleetDecision,
  ScheduledJob,
} from '@/store/api'
