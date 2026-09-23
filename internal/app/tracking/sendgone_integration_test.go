//go:build integration

package tracking

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/platform/botfilter"
)

// The race the retention sweep makes possible: a hit resolves its send, the send
// is deleted, then the insert runs. Postgres refuses the insert on the tenant FK
// (23503), and the store must report that as ErrSendGone — the typed "this send
// is gone" the service answers with a 404 — not as a generic write failure that
// would be logged as an error and still redirect.
func TestRecordEventOnADeletedSendReportsErrSendGone(t *testing.T) {
	ctx := context.Background()
	pool, q, closePool := connect(t)
	defer closePool()
	fx := seedSend(t, ctx, pool, q)
	store := NewPgStore(pool)

	send, ok := store.ResolveSend(ctx, fx.sendID)
	if !ok {
		t.Fatal("fixture send did not resolve")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM sends WHERE id = $1`, fx.sendID); err != nil {
		t.Fatalf("delete send: %v", err)
	}
	err := store.RecordEvent(ctx, Event{
		WorkspaceID: send.WorkspaceID, CampaignID: send.CampaignID, SendID: fx.sendID,
		Kind: botfilter.KindClick, URL: "https://example.test/", UserAgent: "UA",
	})
	if !errors.Is(err, ErrSendGone) {
		t.Fatalf("RecordEvent on a deleted send = %v, want ErrSendGone", err)
	}
}
