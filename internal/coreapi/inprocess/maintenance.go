package inprocess

import "context"

// CleanupExpired removes only long-expired authentication and authorization
// artifacts. Business records are deliberately outside this lifecycle policy.
func (c client) CleanupExpired(ctx context.Context) (int64, error) {
	return c.q.PurgeExpiredSecurityArtifacts(ctx)
}
