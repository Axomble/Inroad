package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// A zero window is `now() - 0s`: every row in the table. The sweep skips a
// disabled table before calling, but the seam must refuse one on its own, so a
// future caller that forgets cannot turn "disabled" into "delete everything".
// These run with a nil query set: the refusal must happen BEFORE any SQL, and a
// nil dereference here would mean it did not.
func TestRetentionBatchesRefuseANonPositiveWindowBeforeAnySQL(t *testing.T) {
	c := client{}
	methods := map[string]func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error){
		"RollupTrackingEvents":      c.RollupTrackingEvents,
		"PurgeDeliverabilityEvents": c.PurgeDeliverabilityEvents,
		"PurgeInboxThreads":         c.PurgeInboxThreads,
		"PurgeSends":                c.PurgeSends,
		"PurgeDeadLetters":          c.PurgeDeadLetters,
	}
	for name, fn := range methods {
		for _, req := range []coreapi.RetentionRequest{
			{OlderThan: 0, Limit: 10},
			{OlderThan: -time.Hour, Limit: 10},
			{OlderThan: time.Hour, Limit: 0},
		} {
			if _, err := fn(context.Background(), req); err == nil {
				t.Errorf("%s(%+v) = nil error, want a refusal", name, req)
			}
		}
	}
}

func TestEncodeRetentionTruncatesToWholeSeconds(t *testing.T) {
	args, err := encodeRetention(coreapi.RetentionRequest{OlderThan: 90*24*time.Hour + 1500*time.Millisecond, Limit: 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Truncating keeps the cutoff a hair LATER, i.e. keeps slightly more.
	if want := int64(90*24*3600 + 1); args.OlderThanSeconds != want {
		t.Fatalf("seconds = %d, want %d", args.OlderThanSeconds, want)
	}
	if !args.AfterAt.Valid {
		t.Fatal("the zero cursor must still be sent as a real timestamp, not NULL (a NULL row comparison matches nothing)")
	}
}
