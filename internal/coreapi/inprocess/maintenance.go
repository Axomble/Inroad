package inprocess

import "context"

// CleanupExpired removes only long-expired authentication and authorization
// artifacts. Business records are deliberately outside this lifecycle policy.
func (c client) CleanupExpired(ctx context.Context) (int64, error) {
	return c.q.PurgeExpiredSecurityArtifacts(ctx)
}

// PurgeIdempotencyKeys removes Idempotency-Key replay-cache rows past their
// fixed 24h retention window (migration 000045). Kept separate from
// CleanupExpired: the idempotency cache is an HTTP-layer concern, not a
// security artifact.
func (c client) PurgeIdempotencyKeys(ctx context.Context) (int64, error) {
	return c.q.PurgeExpiredIdempotencyKeys(ctx)
}

// PurgeWarmupObservations removes immutable warmup evidence past its 90-day
// retention window. The table is append-only and every read window is 30 days or
// less, so anything older serves no policy purpose — and one writer is reachable by
// ANY external sender: a forged warmup token on inbound mail records an
// observer-side row. Without a sweep that is unbounded growth driven by
// unauthenticated input (design §4.6). Kept separate from CleanupExpired, whose doc
// scopes it to authentication artifacts.
func (c client) PurgeWarmupObservations(ctx context.Context) (int64, error) {
	return c.q.PurgeWarmupObservations(ctx)
}

// PurgeDeadWorkers reaps worker-registry rows whose heartbeat stopped long ago,
// plus the mailbox assignments pinned to them. Kept separate from CleanupExpired
// for the same reason as the two above: `workers` is global infrastructure state,
// neither a security artifact nor tenant data.
//
// The assigner already ignores a dead worker's assignment when routing, so this
// is not what keeps mail flowing — it is what stops dead rows from permanently
// skewing the least-loaded pick, which counts assignments per worker.
func (c client) PurgeDeadWorkers(ctx context.Context) (int64, error) {
	return c.q.PurgeDeadWorkers(ctx)
}
