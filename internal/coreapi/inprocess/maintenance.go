package inprocess

import "context"

// CleanupExpired removes only long-expired authentication and authorization
// artifacts. Business records are deliberately outside this lifecycle policy.
func (c client) CleanupExpired(ctx context.Context) (int64, error) {
	return c.q.PurgeExpiredSecurityArtifacts(ctx)
}

// PurgeIdempotencyKeys removes Idempotency-Key replay-cache rows past their
// fixed 24h retention window (migration 000040). Kept separate from
// CleanupExpired: the idempotency cache is an HTTP-layer concern, not a
// security artifact.
func (c client) PurgeIdempotencyKeys(ctx context.Context) (int64, error) {
	return c.q.PurgeExpiredIdempotencyKeys(ctx)
}
