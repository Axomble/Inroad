package inprocess

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/config"
)

// auditPurgeMaxBatches bounds one sweep. Each batch deletes at most 5000 rows
// (PurgeAuditEvents), so a run removes up to 100k rows and a backlog larger
// than that — the first run after retention is switched on for an old
// install — drains over successive daily runs instead of holding one long
// transaction and its locks.
const auditPurgeMaxBatches = 20

// PurgeAuditEvents removes audit rows older than retentionDays, across every
// workspace. It is the ONE path allowed through audit_events' append-only
// trigger: each batch runs in its own transaction that first sets
// inroad.audit_retention_purge (SET LOCAL semantics, so the door closes at
// commit and can never leak into another statement on a pooled connection).
//
// retentionDays <= 0 is "retention disabled" and deletes nothing. The caller
// (maintenance.CleanupHandler) already skips the call in that case; this check
// is the second lock on a destructive door, and the SQL carries a third.
func (c client) PurgeAuditEvents(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	if retentionDays > config.MaxAuditRetentionDays {
		return 0, fmt.Errorf("audit retention of %d days exceeds the supported maximum %d", retentionDays, config.MaxAuditRetentionDays)
	}
	var total int64
	for range auditPurgeMaxBatches {
		var n int64
		err := pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
			qtx := c.q.WithTx(tx)
			if err := qtx.EnableAuditRetentionPurge(ctx); err != nil {
				return err
			}
			var err error
			n, err = qtx.PurgeAuditEvents(ctx, int32(retentionDays)) // bounded by MaxAuditRetentionDays above
			return err
		})
		if err != nil {
			return total, fmt.Errorf("purge audit events: %w", err)
		}
		total += n
		if n < auditPurgeBatch {
			break
		}
	}
	return total, nil
}

// auditPurgeBatch mirrors the LIMIT in the PurgeAuditEvents query: a batch
// shorter than it means the backlog is drained.
const auditPurgeBatch = 5000
